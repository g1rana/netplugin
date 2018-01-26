package networkpolicy

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/contiv/netplugin/contivmodel/client"
	"github.com/contiv/netplugin/utils/k8sutils"
	v1 "k8s.io/api/core/v1"
	network_v1 "k8s.io/api/networking/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const defaultTenantName = "default"
const defaultNetworkName = "default-net"
const defaultSubnet = "10.1.0.0/16"

//const defaultEpgName = "ingress-group"
//const defaultEpgName = "default-epg"
const defaultEpgName = "default-group"
const defaultPolicyName = "ingress-policy"
const defaultRuleID = "1"
const defaultPolicyPriority = 2
const MAX_RETRY = 5

type k8sPodSelector struct {
	TenantName   string //Attach Tenant
	NetworkName  string //Attach network
	GroupName    string //Attach EPG
	PolicyName   string //Attach to policy
	labelPodMap  map[string]map[string]bool
	podIps       map[string]string
	labelSelList []string //List of label string
	groupList    []string
}
type k8sEndPointGroupInfo struct {
	groupsLabel []string
	groupId     uint64
}
type k8sPolicyPorts struct {
	Port     int
	Protocol string
}
type k8sNameSelector struct {
	nameSpaceSel string
}
type podCache struct {
	labeSelector string
	podNetwork   string
	podGroup     string
	podEpg       string
}
type PolicyType string

const (
	// PolicyTypeIngress is a NetworkPolicy that affects ingress
	// traffic on selected pods
	PolicyTypeIngress PolicyType = "Ingress"
	// PolicyTypeEgress is a NetworkPolicy that
	// affects egress traffic on selected pods
	PolicyTypeEgress PolicyType = "Egress"
)

type k8sIPBlockSelector struct {
	CIDR   string
	except []string
}
type k8sNetworkPolicy struct {
	PodSelector k8sPodSelector
	Ingress     []k8sIngressRule
	Egress      []k8sEgressRule
	PolicyTypes []PolicyType
}
type k8sIngressRule struct {
	Ports []k8sPolicyPorts
	From  []k8sNetworkPolicyPeer
}
type k8sEgressRule struct {
	Ports []k8sPolicyPorts
	To    []k8sNetworkPolicyPeer
}
type k8sNetworkPolicyPeer struct {
	IngressPodSelector     *k8sPodSelector
	IngressNameSelector    *k8sNameSelector
	IngressIpBlockSelector *k8sIPBlockSelector
}
type labelPolicySpec struct {
	policy []*k8sNetworkPolicy
}
type npPodInfo struct {
	nameSpace      string
	labelSelectors []string
	IP             string //??? should care for ipv6 address
}
type k8sContext struct {
	k8sClientSet *kubernetes.Clientset
	contivClient *client.ContivClient
	isLeader     func() bool
	//Policy Obj per  Policy Name
	networkPolicy map[string]*k8sNetworkPolicy
	//List of Rules Per Policy
	//	policyRules map[string][]string
	//List of Network configured
	network map[string]bool
	//List of EPG configured as set
	epgName map[string]bool
	//Default  policy Per EPG
	defaultPolicyPerEpg map[string]string
	//List of Policy Per EPG
	policyPerEpg map[string]map[string][]string
	//Cache table for given Pods
	//Policy Obj per  Policy Name
	nwPolicyPerNameSpace map[string]map[string]*k8sNetworkPolicy
	nameSpaceList        map[string]int //NameSpace obj reference
	recalimGroupId       []string
	freeGroupIdIndex     uint64
	epgPerTenant         map[string][]k8sEndPointGroupInfo //list of groups
}

var npLog *log.Entry

func getLabelDBkey(label, nameSpace string) string {
	return label + nameSpace
}

//Start Network Policy feature enabler
func (k8sNet *k8sContext) handleK8sEvents() {
	for k8sNet.isLeader() != true {
		time.Sleep(time.Second * 10)
	}

	errCh := make(chan error)
	for {
		go k8sNet.watchK8sEvents(errCh)

		// wait for error from api server
		errMsg := <-errCh
		npLog.Errorf("%s", errMsg)
		npLog.Warnf("restarting k8s event watch")
		time.Sleep(time.Second * 5)
	}
}

//Create network for given name string
func (k8sNet *k8sContext) createNetwork(nwName, tenant string) error {
	npLog.Infof("create network %s at tenant:%v", nwName, tenant)
	if _, err := k8sNet.contivClient.NetworkGet(
		tenant, nwName); err == nil {
		npLog.Warnf("Network :%v already exist at tenant:%v",
			nwName, tenant)
		return nil
	}

	if err := k8sNet.contivClient.NetworkPost(&client.Network{
		TenantName:  tenant,
		NetworkName: nwName,
		Subnet:      defaultSubnet,
		Encap:       "vxlan",
	}); err != nil {
		npLog.Errorf("failed to create network %s, %s", nwName, err)
		return err
	}

	retry := 0
	for func(int) error {
		_, err := k8sNet.contivClient.NetworkGet(
			tenant,
			nwName)
		return err
	}(retry) != nil {
		//there would be chances on genuine error and
		//it cause infinity  loop
		if retry >= MAX_RETRY {
			return nil
		}
		retry++
		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

//Delete given network from contiv system
func (k8sNet *k8sContext) deleteNetwork(nwName, tenant string) error {
	npLog.Infof("delete network %s from tenant:%v", nwName, tenant)

	if _, err := k8sNet.contivClient.NetworkGet(
		tenant, nwName); err != nil {
		return nil
	}

	if err := k8sNet.contivClient.NetworkDelete(
		tenant, nwName); err != nil {
		npLog.Errorf("failed to delete network %s, %s", nwName, err)
		return err
	}

	rtry := 0
	for func() error {
		_, err := k8sNet.contivClient.NetworkGet(
			tenant, nwName)
		return err
	}(rtry) == nil {
		if rtry >= MAX_RETRY {
			return nil
		}
		rtry++
		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

//Create EPG in context of given Network
func (k8sNet *k8sContext) createEpg(
	tenant,
	nwName,
	epgName string,
	policy []string) error {
	//npLog.Infof("create epg %s policy :%+v", epgName, policy)
	if err := k8sNet.contivClient.EndpointGroupPost(&client.EndpointGroup{
		TenantName:  tenant,
		NetworkName: nwName,
		GroupName:   epgName,
		Policies:    policy,
	}); err != nil {
		npLog.Errorf("failed to create epg %s, %s", epgName, err)
		return err
	}
	rtry := 0
	for func(int) error {
		_, err := k8sNet.contivClient.EndpointGroupGet(
			tenant,
			epgName)
		return err
	}(rtry) != nil {
		if rtry >= MAX_TRY {
			return nil
		}
		rtry++
		time.Sleep(time.Millisecond * 100)
	}
	k8sNet.epgName[epgName] = true
	return nil
}

//Version 1 : Create default EPG at default-net network
func (k8sNet *k8sContext) createEpgInstance(tenant, nwName, epgName string) error {
	var err error
	policy := []string{defaultPolicyName}
	if err = k8sNet.createDefaultPolicy(
		defaultTenantName,
		epgName); err != nil {
		npLog.Errorf("failed  Default ingress policy EPG %v: err:%v ",
			epgName, err)
		return err
	}
	if err = k8sNet.createEpg(tenant, nwName, epgName, policy); err != nil {
		npLog.Errorf("failed to update EPG %v: err:%v ", epgName, err)
		return err
	}
	policyMap := k8sNet.policyPerEpg[epgName]
	if len(policyMap) <= 0 {
		policyMap = make(map[string][]string, 0)
	}
	//Build Default policy and Assign Default Rule
	policyMap[defaultPolicyName] = append(policyMap[defaultPolicyName],
		defaultRuleID)
	//Assign defult Policy to Newly Created Group
	k8sNet.policyPerEpg[epgName] = policyMap
	return err
}

//Delete EPG from given network
func (k8sNet *k8sContext) deleteEpg(tenant, networkname,
	epgName, policyName string) error {
	npLog.Infof("delete epg %s", epgName)
	if _, err := k8sNet.contivClient.
		EndpointGroupGet(tenant, epgName); err != nil {
		return nil
	}

	if err := k8sNet.contivClient.EndpointGroupDelete(
		tenant, epgName); err != nil {
		npLog.Errorf("failed to delete epg %s, %s", epgName, err)
		return err
	}
	rtry := 0
	for func(int) error {
		_, err := k8sNet.contivClient.
			EndpointGroupGet(tenant, epgName)
		return err
	}(rtry) == nil { //Same as above
		if rtry >= MAX_RETRY {
			return nil
		}
		rtry++
		time.Sleep(time.Millisecond * 100)
	}

	delete(k8sNet.epgName, epgName)

	policyMap := k8sNet.policyPerEpg[epgName]
	for pName, policy := range policyMap {
		for _, ruleId := range policy {
			//XXX:Trigger Rule Delete Request in configured
			k8sNet.deleteRule(tenant, pName, ruleId)
		}
		k8sNet.deletePolicy(pName)
		delete(policyMap, pName)
	}
	delete(k8sNet.policyPerEpg, epgName)
	return nil
}

//Create policy contiv system
func (k8sNet *k8sContext) createPolicy(tenantName string,
	policyName string) error {
	if _, err := k8sNet.contivClient.
		PolicyGet(tenantName, policyName); err == nil {
		npLog.Infof("Policy:%v found contiv", policyName)
		return err
	}
	if err := k8sNet.contivClient.PolicyPost(&client.Policy{
		TenantName: tenantName,
		PolicyName: policyName,
	}); err != nil {
		npLog.Errorf("failed to create policy: %v", err)
		return err
	}
	rtry := 0
	for func(int) error {
		_, err := k8sNet.contivClient.PolicyGet(tenantName, policyName)
		return err
	}(rtry) != nil {
		if rtry >= MAX_RETRY {
			return nil
		}
		time.Sleep(time.Millisecond * 100)
	}
	//policyMap := k8sNet.policyPerEpg[epgName]
	//Attach newly created policy to EPG
	//policyMap[policyName] = []string{}
	return nil
}

//Delete given policy from Contiv system
func (k8sNet *k8sContext) deletePolicy(tenant, policyName string) error {
	npLog.Infof("delete policy %s from tenant %s ", policyName, tenant)

	if _, err := k8sNet.contivClient.
		PolicyGet(tenant, policyName); err != nil {
		return nil
	}

	if err := k8sNet.contivClient.
		PolicyDelete(tenant, policyName); err != nil {
		npLog.Errorf("failed to delete policy %s, %s", policyName, err)
		return err
	}

	rtry := 0
	for func(int) error {
		_, err := k8sNet.contivClient.PolicyGet(defaultTenantName,
			policyName)
		return err
	}(rtry) == nil {
		if rtry >= MAX_RETRY {
			return nil
		}
		rtry++
		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

//Post rule  to contiv if not exist
func (k8sNet *k8sContext) createRule(cRule *client.Rule) error {

	if val, err := k8sNet.contivClient.RuleGet(cRule.TenantName,
		cRule.PolicyName, cRule.RuleID); err == nil {
		if val.Action != cRule.Action {
			k8sNet.deleteRule(cRule.TenantName,
				cRule.PolicyName, cRule.RuleID)
		} else {
			npLog.Infof("Rule:%+v already exist", *cRule)
			return nil
		}
	}

	if err := k8sNet.contivClient.RulePost(cRule); err != nil {
		npLog.Errorf("failed to create rule: %s, %v", cRule.RuleID, err)
		return err
	}
	rtry := 0
	for func(int) error {
		_, err := k8sNet.contivClient.RuleGet(cRule.TenantName,
			cRule.PolicyName, cRule.RuleID)
		return err
	}(rtry) != nil {
		if retry >= MAX_RETRY {
			return nil
		}
		retry++
		time.Sleep(time.Millisecond * 100)

	}
	return nil
}

//Delete rule from contiv system
func (k8sNet *k8sContext) deleteRule(tenantName string,
	policyName, ruleID string) error {
	npLog.Infof("Delete rule: %s:%s", ruleID, policyName)

	if _, err := k8sNet.contivClient.
		RuleGet(tenantName, policyName, ruleID); err != nil {
		return nil
	}

	if err := k8sNet.contivClient.
		RuleDelete(tenantName, policyName, ruleID); err != nil {
		npLog.Errorf("Failure rule del Ops:%s:%s,%v",
			ruleID, policyName, err)
		return err
	}

	return nil
}
func (k8sNet *k8sContext) processK8sNamespace(
	opCode watch.EventType, ns *v1.Namespace) {
	if np.Namespace == "kube-system" { //skip system namespace
		return
	}
	npLog.Infof("Recv Namespace resource Update[%+v]:%+v ", opCode, *ns)
	switch opCode {
	case watch.Added, watch.Modified:
		k8sNet.addNamespace(ns)
	case watch.Deleted:
		k8sNet.deleteNamespace(ns)
	}
}
func (k8sNet *k8sContext) addNamespace(ns *v1.Namespace) {
	npLog.Infof("Add NameSpace request :%v", ns.Namespace)
	if err := k8sNet.contivClient.TenantPost(
		&contivClient.Tenant{TenantName: ns.Namespace}); err != nil {
		npLog.Errorf("Failed to create Tenant for :%v namespace",
			ns.Namespace)
		return
	}
	npLog.Infof("Tenant created for NameSpace :%v", ns.Namespace)
}
func (k8sNet *k8sContext) deleteNamespace(ns *v1.Namespace) {
	npLog.Infof("Delete NameSpace request: %+v", ns.Namespace)
	if err := k8sNet.contivClient.TenantDelete(
		&contivClient.Tenant{TenantName: ns.Namespace}); err != nil {
		npLog.Errorf("Failed to delete Tenant for :%v namespace",
			ns.Namespace)
		return
	}
	npLog.Infof("Tenant deleted for Namespace %v", ns.Namespace)
}

//Sub handler to process  Network Policy event from K8s srv
func (k8sNet *k8sContext) perocessK8sNetworkPolicy(
	opCode watch.EventType, np *network_v1.NetworkPolicy) {
	if np.Namespace == "kube-system" { //not applicable for system namespace
		return
	}

	npLog.Infof("Network Policy[%v]: %+v", opCode, *np)
	switch opCode {
	case watch.Added, watch.Modified:
		k8sNet.addNetworkPolicy(np)

	case watch.Deleted:
		k8sNet.delNetworkPolicy(np)
	}
}

//Sub Handler to process Pods events from K8s srv
func (k8sNet *k8sContext) processK8sPods(opCode watch.EventType, pod *v1.Pod) {
	if pod.Namespace == "kube-system" { //not applicable for system namespace
		return
	}
	//K8s pods event doesn't provide Ips information in Add/delete type
	//opcode
	//npLog.Infof("K8s Event : POD [%s],NameSpace[%v] ,Label:[%+v],IPs:[%v]",
	//	opCode, pod.ObjectMeta.Namespace,
	//	pod.ObjectMeta.Labels, pod.Status.PodIP)

	if _, ok := k8sNet.
		nwPolicyPerNameSpace[pod.ObjectMeta.Namespace]; !ok {
		npLog.Infof("Pod doesn't match policy namespace")
		return
	}
	switch opCode {
	case watch.Added, watch.Modified, watch.Deleted:
		if pod.ObjectMeta.DeletionTimestamp != nil &&
			len(pod.Status.PodIP) > 0 {
			//K8s Srv notify Pods delete case as part of modify
			//event by specifying DeletionTimeStamp
			// Pod Delete event doesn't carry Pod Ips info
			//therefore Using Modify event to manipulate future
			//delete event
			k8sNet.processPodDeleteEvent(pod)
		} else if len(pod.Status.PodIP) > 0 {
			//Pod event without timeDeletion with Pod ip consider
			//as pod add event
			k8sNet.processPodAddEvent(pod)
		}

	}
}

//Parse Pod Info from receive Pod events
func parsePodInfo(pod *v1.Pod) npPodInfo {
	var pInfo npPodInfo
	pInfo.nameSpace = pod.ObjectMeta.Namespace
	for key, val := range pod.ObjectMeta.Labels {
		pInfo.labelSelectors =
			append(pInfo.labelSelectors, (key + "=" + val))
	}
	pInfo.IP = pod.Status.PodIP
	return pInfo
}

//lookup Group lebel in per tanant cache table
func (k8sNet *k8sContext) lookupGroupFromLabelsSet(nameSpace string,
	labelSelectors []string) {
	for group := range k8sNet.epgPerTenant[nameSpace] {
		if isGroupSliceEqual(group, labelSelectors) {
			return true
		}
	}
	return false
}

//Check if given 2 slice has same content regardless of their order
func isGroupSliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	for i := range a {
		found := false
		for j := range b {
			if i == j {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

//Create Group obj using receiving pod Selector info
func (k8sNet *k8sContext) createGroupFromLabelsSet(nameSpace string,
	labelSelectors []string) k8sEndPointGroupInfo {
	groupId := 0
	if len(k8sNet.recalimGroupId) > 0 {
		groupId = k8sNet.recalimGroupId[0]
		k8sNet.recalimGroupId = k8sNet.recalimGroupId[1:]
	}
	if groupId <= 0 {
		groupId = k8sNet.freeGroupIdIndex
		k8sNet.freeGroupIdIndex++
	}
	groups := k8sEndPointGroupInfo{groupsLabel: labelSelectors,
		groupId: groupId}
	k8sNet.epgPerTenant[nameSpace] = append(k8sNet.epgPerTenant[nameSpace],
		groups)
	return groups
}

//XXX: this API  internally  do following
//Case 1: Call some external API which return it Set of EndPoint Group
//Which has been created and matched with given Label String Set
//Case 2 : Call state driver ETCD  for GroupList Obj
//Current Below API should be deprecated  and currently used only for
//Code understanding
func (k8sNet *k8sContext) getGroupSetFromLabelSet(nameSpace string,
	labelSelectors []string) []uint64 {
	groupMap := make(map[uint64]bool, 0)
	groupList := k8sNet.epgPerTenant[nameSpace] //List of groups in namespace
	for label := range labelSelectors {
		for group := range groupList {
			for labelSel := range group.groupsLabel {
				if labelSel == label {
					groupMap[group.groupId] = true
				}
			}
		}
	}
	var groupIdSet []uint64
	for k, _ := range groupMap {
		groupIdSet = append(groupIdSet, k)
	}
	return groupIdSet
}

//Get  Network Policy object sets which ToSpec labelMap information match
//with given pods labelMap
func (k8sNet *k8sContext) getMatchToSpecPartNetPolicy(
	podInfo npPodInfo) []*k8sNetworkPolicy {
	var toPartPolicy []*k8sNetworkPolicy
	nwPolicyMap, ok := k8sNet.nwPolicyPerNameSpace[podInfo.nameSpace]
	if !ok {
		npLog.Warnf("No NetworkPolicy for NameSpace:%v",
			podInfo.nameSpace)
		return nil
	}
	for _, nwPol := range nwPolicyMap {
		for _, label := range podInfo.labelSelectors {
			//Collect networkPolicy object which match with pods
			//Labels
			if _, ok := nwPol.PodSelector.labelPodMap[label]; ok {
				toPartPolicy = append(toPartPolicy, nwPol)
				//	npLog.Infof("policy :%+v", nwPol)
				break
			}
		}
	}
	return toPartPolicy
}

//Get Network Policy object sets which FromSpec, labelMap information match
//with given pods labelMap
func (k8sNet *k8sContext) getMatchFromSpecPartNetPolicy(
	podInfo npPodInfo) []*k8sNetworkPolicy {

	var fromPartPolicy []*k8sNetworkPolicy
	//NetworkPolicy master object   on pods Namespace
	nwPolicyMap, ok := k8sNet.nwPolicyPerNameSpace[podInfo.nameSpace]
	if !ok {
		npLog.Infof("Pod namespace doesn't have any policy config")
		return nil
	}
	//Build list of networkPolicy object which fromSpec belongs to given
	//pods Info
	for _, l := range podInfo.labelSelectors {
		for _, nwPol := range nwPolicyMap {
			for _, ingress := range nwPol.Ingress {
				//PodSelector on FromSpec part of policy Object
				for _, podSelector := range ingress.
					IngressPodSelector {
					npLog.Infof("labelMap:%+v",
						podSelector.labelPodMap)
					if _, ok :=
						podSelector.labelPodMap[l]; ok {
						fromPartPolicy =
							append(fromPartPolicy,
								nwPol)
						break
					}
				}
			}
		}
	}
	return fromPartPolicy
}

//Process Pod Delete Event from K8s Srv
func (k8sNet *k8sContext) processPodDeleteEvent(pod *v1.Pod) {
	labelList := podInfo.labelSelectors
	npLog.Infof("POD [Delete] for pods:%+v", pod)
	//find All configured Network Policy object which given pods LableMap
	//match

	toSpecNetPolicy := k8sNet.getMatchToSpecPartNetPolicy(podInfo)
	if len(toSpecNetPolicy) > 0 {
		for _, nw := range toSpecNetPolicy {

			//Get Target PodSelector Groupset Info
			toGroupInfo := k8sNet.getGroupSetFromLabelSet(nw.PodSelector.TenantName,
				podInfo.labelSelectors)
			//Convert ToGroupInfo Id into string Slice
			toGroup := []string{}
			for id := range toGroupInfo {
				toGroup = append(toGroup, strconv.Itoa(id))
			}
			rList := k8sNet.
				buildRuleFromIngressGroupSet(nw,
					nw.PodSelector.PolicyName)
			//Build Final policy rule and pushed to Contiv system
			ruleList := k8sNet.finalGroupBaseNetworkPolicyRule(np, toGroupSet,
				rList, false)
			npLog.Infof("final rules :%v", ruleList)
			npLog.Infof("Delete To Spec rule:%+v", ruleList)
		}
	} else { //Pods  belongs fromPart of Spec
		fromPartPolicy := k8sNet.getMatchFromSpecPartNetPolicy(podInfo)
		npLog.Infof("Delete Pod belong FromSec part:%+v",
			fromPartPolicy)
		if len(fromPartPolicy) > 0 {
			npLog.Infof("remove PodIps:%v fromSpec part of Policy",
				rmIps)
			for _, nw := range fromPartPolicy {
				rList := k8sNet.
					buildRuleFromIngressGroupSet(nw,
						nw.PodSelector.PolicyName)
				npLog.Infof("Ingress Rule :%+v", *rList)
				//Build Final policy rule and pushed to Contiv system
				ruleList := k8sNet.finalGroupBaseNetworkPolicyRule(np, toGroupSet,
					rList, false)
				npLog.Infof("Pod rules:%+v", ruleList)
			}
		}
	}
}
func getIpMapToSlice(m map[string]string) []string {
	ips := []string{}
	for ip := range m {
		ips = append(ips, ip)
	}
	return ips
}
func (k8sNet *k8sContext) UpdateIpListFromSpecfromLabel(nw *k8sNetworkPolicy,
	label []string, ip string) {
	for _, ingress := range nw.Ingress {
		for _, podSelector := range ingress.IngressPodSelector {
			for _, l := range label {
				if ipMap, ok :=
					podSelector.labelPodMap[l]; ok {
					ipMap[ip] = true
				}
			}
			//Rebuild PodSelector PodIps
			k8sNet.updatePodSelectorPodIps(podSelector)
			npLog.Infof("Update PodIps into FromSpecPod:%+v",
				podSelector)
		}
	}
	return
}

//Remove Give Pod Ips fromSpec Object of Network Policy
func (k8sNet *k8sContext) rmIpFromSpecPodSelector(
	nw *k8sNetworkPolicy, label []string, ip string) {
	for _, ingress := range nw.Ingress {
		for _, podSelector := range ingress.IngressPodSelector {
			npLog.Infof("podSelector:%+v", podSelector)
			//remove ips from PodSelector Object
			delete(podSelector.podIps, ip)
			for _, l := range label {
				if ipMap, ok :=
					podSelector.labelPodMap[l]; ok {
					delete(ipMap, ip)
					npLog.Infof("remove Pod Ips:%v FromSpec map:%+v",
						ip, ipMap)
				}
			}
			//Rebuild PodSelector PodIps
			k8sNet.updatePodSelectorPodIps(podSelector)
		}
	}
	return
}

//Process Pod Add event ; Modify version based On group Info
func (k8sNet *k8sContext) processPodAddEvent(pod *v1.Pod) {
	if pod.Status.PodIP == "" {
		return
	}
	//Get Pods Ips and its Pod selector label
	podInfo := parsePodInfo(pod)
	if k8sNet.nameSpaceList[podInfo.nameSpace]; !ok {
		//Create Tenant obj wrt namespace
		if err := k8sNet.createTenant(podInfo.nameSpace); err != nil {
			npLog.Errorf("Failed to create Tenant %v ",
				podInfo.nameSpace)
			return
		}
		npLog.Infof("Create Tenant %v in contiv system", podInfo.nameSpace)
	}
	k8sNet.nameSpaceList[podInfo.nameSpace]++
	npLog.Infof("Lookup Pod  Group in contiv system")

	//lookup for group for recv pod if not found then create new one and
	//cache it
	//XXX: Below block should just call APIs to get GroupSet Info
	//by passing Label selector information of Pods
	//That APis either should be implemented in Netmaster or
	//Use state Driver object to get this Pods Group set information
	if ok := k8sNet.lookupGroupFromLabelsSet(podInfo.nameSpace,
		podInfo.labelSelectors); !ok {
		//Looks like new group info
		npLog.Infof("New Group info:%v with pod recv", podInfo.labelSelectors)
		info := k8sNet.createGroupFromLabelsSet(podInfo.nameSpace,
			podInfo.labelSelectors)
		npLog.Infof("Recv group info :%v", info)
	}

	npLog.Infof("POD [ADD] request for pod %+v", pod)
	//get programmed  NetworkPolicy for recv Pod Namespace
	toPartPolicy := k8sNet.getMatchToSpecPartNetPolicy(podInfo)
	npLog.Infof("ToPartSpec:%+v", toPartPolicy)

	if len(toPartPolicy) > 0 {
		//Pods belongs to Policy which has Pods Label as Target
		npLog.Infof("Recv Pod belongs to ToSpec part of Policy")
		//Walk all found Policy
		for _, nw := range toPartPolicy {
			rList := k8sNet.buildRulesFromIngressSpec(nw,
				nw.PodSelector.PolicyName)
			if len(*rList) > 0 {
				npLog.Infof("Pods Info in To Spec :%+v",
					nw.PodSelector.podIps)
				//Get Target PodSelector Groupset Info
				toGroupInfo := k8sNet.getGroupSetFromLabelSet(np.PodSelector.TenantName,
					podInfo.labelSelectors)
				//Convert ToGroupInfo Id into string Slice
				toGroup := []string{}
				for id := range toGroupInfo {
					toGroup = append(toGroup, strconv.Itoa(id))
				}

				ruleList := k8sNet.finalGroupBaseNetworkPolicyRule(nw, toGroup, rList, true)
				npLog.Infof("To Spec Pod rules:%+v", ruleList)
				npLog.Infof("podInf.labelSelectors:%+v",
					podInfo.labelSelectors)
			}
		}
	} else {
		//Build fromPodSelector List
		fromPartPolicy := k8sNet.getMatchFromSpecPartNetPolicy(podInfo)
		//Build Rules and update to OVS
		for _, nw := range fromPartPolicy {
			npLog.Infof("fromPartPolicy:%+v", *nw)
			rList := k8sNet.buildRulesFromIngressSpec(nw,
				nw.PodSelector.PolicyName)
			if len(*rList) > 0 {
				npLog.Infof("Ingress Rule :%+v", *rList)
				npLog.Infof("Pods Info in To Spec :%+v",
					nw.PodSelector.podIps)
				ipList := getIpMapToSlice(nw.PodSelector.podIps)
				ruleList := k8sNet.finalGroupBaseNetworkPolicyRule(nw, toGroup,
					rList, true)
				npLog.Infof("Pod rules:%+v", ruleList)
			}
		}
	}
}

//Handler to process APIs Server Watch event
func (k8sNet *k8sContext) processK8sEvent(opCode watch.EventType,
	eventObj interface{}) {
	//Only Leader will process events
	if k8sNet.isLeader() != true {
		return
	}

	switch objType := eventObj.(type) {

	case *v1.Pod:
		k8sNet.processK8sPods(opCode, objType)
	case *v1.Namespace:
		k8sNet.processK8sNamespace(opCode, objType)
	case *network_v1.NetworkPolicy:
		k8sNet.processK8sNetworkPolicy(opCode, objType)
	default:
		npLog.Infof("Unwanted event from K8s evType:%v objType:%v",
			opCode, objType)
	}
}

func (k8sNet *k8sContext) watchK8sEvents(errChan chan error) {
	var selCase []reflect.SelectCase

	// wait to become leader
	for k8sNet.isLeader() != true {
		time.Sleep(time.Millisecond * 100)
	}
	//Set Watcher for Network Policy resource
	npWatch, err := k8sNet.k8sClientSet.Networking().
		NetworkPolicies("").Watch(meta_v1.ListOptions{})
	if err != nil {
		errChan <- fmt.Errorf("failed to watch network policy, %s", err)
		return
	}

	selCase = append(selCase, reflect.SelectCase{Dir: reflect.SelectRecv,
		Chan: reflect.ValueOf(npWatch.ResultChan())})
	//Set watcher for Pods resource
	podWatch, _ := k8sNet.k8sClientSet.CoreV1().
		Pods("").Watch(meta_v1.ListOptions{})

	selCase = append(selCase, reflect.SelectCase{Dir: reflect.SelectRecv,
		Chan: reflect.ValueOf(podWatch.ResultChan())})

	//Set Watcher for namespace creation
	nsWatch, err := k8sNet.k8sClientSet.CoreV1().
		NameSpace("").Watch(meta_v1.ListOptions{})
	if err != nil {
		errChan <- fmt.Errorf("failed to watch NameSpace resource %s", err)
		return
	}

	selCase = append(selCase, reflect.SelectCase{Dir: reflect.SelectRecv,
		Chan: reflect.ValueOf(nsWatch.ResultChan())})

	for {
		_, recVal, ok := reflect.Select(selCase)
		if !ok {
			// channel closed, trigger restart
			errChan <- fmt.Errorf("channel closed to k8s api server")
			return
		}

		if k8sNet.isLeader() != true {
			continue
		}

		if event, ok := recVal.Interface().(watch.Event); ok {
			k8sNet.processK8sEvent(event.Type, event.Object)
		}
		// ignore other events
	}
}

// InitK8SServiceWatch monitor k8s services
func InitK8SServiceWatch(listenURL string, isLeader func() bool) error {
	npLog = log.WithField("k8s", "netpolicy")

	listenAddr := strings.Split(listenURL, ":")
	if len(listenAddr[0]) <= 0 {
		listenAddr[0] = "localhost"
	}
	contivClient, err := client.NewContivClient("http://" + listenAddr[0] + ":" + listenAddr[1])
	if err != nil {
		npLog.Errorf("failed to create contivclient %s", err)
		return err
	}

	k8sClientSet, err := k8sutils.SetUpK8SClient()
	if err != nil {
		npLog.Fatalf("failed to init K8S client, %v", err)
		return err
	}
	//nwoPolicyDb := make(map[string]k8sNetworkPolicy, 0)
	kubeNet := k8sContext{
		contivClient:  contivClient,
		k8sClientSet:  k8sClientSet,
		isLeader:      isLeader,
		networkPolicy: make(map[string]*k8sNetworkPolicy, 0),
		//lookup table for Configured Network;
		network: make(map[string]bool, 0),
		//lookup table for Configured Policy per EPG
		defaultPolicyPerEpg: make(map[string]string, 0),
		epgName:             make(map[string]bool, 0),
		policyPerEpg:        make(map[string]map[string][]string, 0),
		//policyRules:          make(map[string][]string, 0),
		nwPolicyPerNameSpace: make(map[string]map[string]*k8sNetworkPolicy, 0),
	}

	//Trigger default epg : = default-group
	kubeNet.createEpgInstance(defaultNetworkName, defaultEpgName)

	go kubeNet.handleK8sEvents()
	return nil
}
func getLabelSelector(key, val string) string {
	return (key + "=" + val)
}

func (k8sNet *k8sContext) addNetworkPolicy(np *network_v1.NetworkPolicy) {
	//check if given Policy already exist
	if _, ok := k8sNet.networkPolicy[np.Name]; ok {
		npLog.Warnf("Delete existing network policy: %s !", np.Name)
		k8sNet.delNetworkPolicy(np)
	}

	//Target Pod Selector Label Parsing and GroupSet lookup
	npPodSelector, err := k8sNet.parsePodSelectorForGroupSet(
		np.Spec.PodSelector.MatchLabels,
		np.Namespace)
	if err != nil {
		npLog.Warnf("ignore network policy: %s, %v", np.Name, err)
		return
	}
	//Set policy name ToSpec podSelector Obj
	npPodSelector.PolicyName = np.Name
	//Save recv Label map info
	npLog.Infof("Network  policy [%s] pod-selector: %+v",
		np.Name, npPodSelector)

	for policyType := range np.Spec.PolicyTypes {
		//XXX: This version only support Ingress NetworkPolicy
		if policyType == PolicyTypeIngress {
			//Parse Ingress Policy
			IngressRules, err :=
				k8sNet.parseIngressPolicy(np.Spec.Ingress,
					np.Namespace)
			if err != nil {
				npLog.Warnf("ignore network policy: %s, %v", np.Name, err)
				return
			}
			nwPolicy := k8sNetworkPolicy{
				PodSelector: npPodSelector, //Target PodSelector
				Ingress:     IngressRules}

			npLog.Info("Apply nwPolicy[%s] TargetPod:%+v Ingress:%+v",
				np.Name, npPodSelector, IngressRules)

			//Push policy info to ofnet agent
			if err := k8sNet.applyContivNetworkPolicy(&nwPolicy); err != nil {
				npLog.Errorf("[%s] failed to configure policy, %v",
					np.Name, err)
				return
			}
			//Cache configued NetworkPolicy obj using policy Name
			k8sNet.networkPolicy[np.Name] = &nwPolicy
			//cache networkPolicy obj per namespace
			if _, ok := k8sNet.nwPolicyPerNameSpace[np.Namespace]; !ok {
				k8sNet.nwPolicyPerNameSpace[np.Namespace] =
					make(map[string]*k8sNetworkPolicy, 0)
			}
			nwPolicyMap, _ := k8sNet.nwPolicyPerNameSpace[np.Namespace]
			nwPolicyMap[np.Name] = &nwPolicy
		}
	}

	//append(k8sNet.nwPolicyPerNameSpace[np.Name], &nwPolicy)
	npLog.Infof("Add network policy in per NameSpace:%v",
		k8sNet.nwPolicyPerNameSpace[np.Namespace])
}

//Build partial rule using FromSpec Using GroupSet Info
func (k8sNet *k8sContext) buildRuleFromIngressGroupSet(
	np *k8sNetworkPolicy,
	policyName string) (lRules *[]client.Rule) {

	var listRules []client.Rule
	for policyType := range np.Spec.PolicyTypes {
		//XXX: This version only support Ingress NetworkPolicy
		if policyType == PolicyTypeIngress {
			for _, ingress := range np.Ingress {
				isPortsCfg := false
				if len(ingress.Ports) > 0 {
					isPortsCfg = true
					//Is Port Cfg included into From Ingress Spec
				}
				for _, from := range ingress.From {
					//PodSelector label
					if from.IngressPodSelector != nil {
						//Pod Selector case
						groupSet := from.IngressPodSelector.groupList
						for _, fromGroup := range groupSet {
							rule := client.Rule{
								TenantName:        np.PodSelector.TenantName,
								PolicyName:        np.PodSelector.PolicyName,
								FromEndpointGroup: fromGroup,
								Priority:          defaultPolicyPriority,
								Direction:         "in",
								Action:            "allow"}
							//If Port cfg enable
							if isPortsCfg {
								for _, p := range ingress.Ports {
									k8sNet.appendPolicyPorts(&rule, p)
									listRules = append(listRules, rule)
								}
							} else {
								listRules = append(listRules, rule)
							}
						}
					} else if from.IngressIpBlockSelector != nil {
						//CIDR case
						rule := client.Rule{
							TenantName:  np.PodSelector.TenantName,
							PolicyName:  np.PodSelector.PolicyName,
							FromNetwork: from.IngressIpBlockSelector.CIDR,
							Priority:    defaultPolicyPriority,
							Direction:   "in",
							Action:      "allow"}
						//If Port cfg enable
						if isPortsCfg {
							for _, p := range ingress.Ports {
								k8sNet.appendPolicyPorts(&rule, p)
								listRules = append(listRules, rule)
							}
						} else {
							listRules = append(listRules, rule)
						}
					} else if from.IngressNameSelector != nil {
						//XXX: NameSpace Selector
					}
				}

			}
		}

	}
	return &listRules
}

//Build partial rule list using FromSpec PodSelector information
func (k8sNet *k8sContext) buildRulesFromIngressSpec(
	np *k8sNetworkPolicy,
	policyName string) (lRules *[]client.Rule) {
	var listRules []client.Rule
	for _, ingress := range np.Ingress {
		isPortsCfg := false
		if len(ingress.IngressRules) > 0 {
			isPortsCfg = true
			//Is Port Cfg included into From Ingress Spec
		}
		//Ingress Pod Selector
		for _, podSec := range ingress.IngressPodSelector {
			for _, fromIp := range podSec.podIps {
				rule := client.Rule{
					TenantName:    np.PodSelector.TenantName,
					PolicyName:    np.PodSelector.PolicyName,
					FromIpAddress: fromIp,
					Priority:      defaultPolicyPriority,
					Direction:     "in",
					Action:        "allow"}
				//If Port cfg enable
				if isPortsCfg {
					for _, p := range ingress.IngressRules {
						k8sNet.appendPolicyPorts(&rule, p)
						listRules = append(listRules, rule)
					}
				} else {
					listRules = append(listRules, rule)
				}
			}
		}
	}
	return &listRules
}

//Build Partial Rules based on  FromSpec Pod IPs list
func (k8sNet *k8sContext) buildIngressRuleToPodSelector(
	np *k8sNetworkPolicy, //Network Policy object
	from []string, //FromSpec Ips List
	policyName string) (lRules *[]client.Rule) {

	//npLog.Infof("From:%v", from)
	var listRules []client.Rule
	//Walk info Ingress Policy FromSpec PodSelector
	for _, ingress := range np.Ingress {
		isPortsCfg := false
		if len(ingress.IngressRules) > 0 {
			isPortsCfg = true
			//Is Port Cfg included into From Ingress Spec
		}
		//Attach All fromSpec IpsList
		for _, fromIp := range from {
			rule := client.Rule{
				TenantName:    np.PodSelector.TenantName,
				PolicyName:    np.PodSelector.PolicyName,
				FromIpAddress: fromIp,
				Priority:      defaultPolicyPriority,
				Direction:     "in",
				Action:        "allow"}
			if isPortsCfg {
				for _, port := range ingress.IngressRules {
					//Add port Info into Rule
					k8sNet.appendPolicyPorts(&rule, port)
					listRules = append(listRules, rule)
				}
			} else {
				listRules = append(listRules, rule)
			}
		}
	}
	return &listRules
}

//final Build Rules Using Group set inforamtion
func (k8sNet *k8sContext) finalGroupBaseNetworkPolicyRule(np *k8sNetworkPolicy,
	toGroupSet []string,
	ingressRules []client.Rule,
	isAdd bool) *[]client.Rule {

	var err error
	ruleList := ingressRules

	//Walk to all target Pod Group selector list
	for _, toGroup := range toGroupSet {
		npLog.Infof("ruleList:%v", ruleList)
		//Complete partial rules by adding Target Pods groups
		for _, rule := range ruleList {
			rule.ToEndpointGroup = toGroup
			if rule.FromEndpointGroup != nil {
				rule.RuleID = k8sutils.PolicyToRuleIDUsingGroups(
					toGroup, rule.FromEndpointGroup,
					rule.Port, rule.Protocol,
					np.PodSelector.PolicyName)
			} else if rule.FromNetwork != nil {
				rule.RuleID = k8sutils.PolicyToRuleIDUsingGroups(
					toGroup, rule.FromNetwork,
					rule.Port, rule.Protocol,
					np.PodSelector.PolicyName)

			}
			//XXX: Namespace Selector needs to be handled here

			npLog.Infof("RulID:%v", rule.RuleID)
			//Update Policy Name cache with policy Id
			if isAdd {
				if err = k8sNet.createRule(&rule); err != nil {
					npLog.Errorf("failed: rules in-policy %+v, %v",
						np.PodSelector, err)
					return nil
				}
			} else { //Policy Delete
				if err = k8sNet.deleteRule(rule.TenantName,
					rule.PolicyName,
					rule.RuleID); err != nil {
					npLog.Errorf("failed: del in-policy %+v, %v",
						np.PodSelector, err)
					return nil
				}
			}
			//Get Policy Map Table for given EPG
			policyMap := k8sNet.policyPerEpg[np.PodSelector.GroupName]
			//Get Rule Id Sets for given Policy
			policy := policyMap[np.PodSelector.PolicyName]
			if isAdd {
				policy = append(policy, rule.RuleID)
				npLog.Infof("RuleId:%v is added into Policy:%v",
					rule.RuleID, np.PodSelector.PolicyName)
				policyMap[np.PodSelector.PolicyName] = policy
			} else {
				for idx, r := range policy {
					if r == rule.RuleID {
						policy = append(policy[0:idx],
							policy[idx+1:]...)
						npLog.Infof("RuleId :%v deleted ",
							rule.RuleID)
						break
					}
				}
			}
		}
	}
	return &ruleList
}

//final Build Rules by linking from spec to To spec podSelector
func (k8sNet *k8sContext) finalIngressNetworkPolicyRule(np *k8sNetworkPolicy,
	toPodIPs []string,
	ingressRules []client.Rule,
	isAdd bool) *[]client.Rule {

	var err error
	ruleList := ingressRules

	//policyCtx := k8sNet.policyRules[np.PodSelector.PolicyName]

	//Ingress Spec To section Pods
	for _, toIps := range toPodIPs {
		//npLog.Infof("ruleList:%v", ruleList)
		//Rebuild Rule List to add To Ips
		for _, rule := range ruleList {
			//Update To src Ip section in Rule
			rule.ToIpAddress = toIps
			//Generate RuleID :XXX: Should look for better approach
			rule.RuleID = k8sutils.PolicyToRuleIDUsingIps(
				toIps, rule.FromIpAddress,
				rule.Port, rule.Protocol,
				np.PodSelector.PolicyName)

			npLog.Infof("RulID:%v", rule.RuleID)
			//Update Policy Name cache with policy Id
			if isAdd {
				if err = k8sNet.createRule(&rule); err != nil {
					npLog.Errorf("failed: rules in-policy %+v, %v",
						np.PodSelector, err)
					return nil
				}
				//Update RuleID in cache Db
				//XXX:Should use Hash Set instead of slice
				// to aviod duplicate Ruleid insertion
				//policyCtx = append(policyCtx, rule.RuleID)
			} else { //Policy Delete
				if err = k8sNet.deleteRule(rule.TenantName,
					rule.PolicyName,
					rule.RuleID); err != nil {
					npLog.Errorf("failed: del in-policy %+v, %v",
						np.PodSelector, err)
					return nil
				}
			}
			//Get Policy Map Table for given EPG
			policyMap := k8sNet.policyPerEpg[np.PodSelector.GroupName]
			//Get Rule Id Sets for given Policy
			policy := policyMap[np.PodSelector.PolicyName]
			if isAdd {
				policy = append(policy, rule.RuleID)
				npLog.Infof("RuleId:%v is added into Policy:%v",
					rule.RuleID, np.PodSelector.PolicyName)
				policyMap[np.PodSelector.PolicyName] = policy
			} else {
				for idx, r := range policy {
					if r == rule.RuleID {
						policy = append(policy[0:idx],
							policy[idx+1:]...)
						npLog.Infof("RuleId :%v deleted ",
							rule.RuleID)
						break
					}
				}
			}
		}
	}
	return &ruleList
}

//Build policy ,rules and attached it to EPG
func (k8sNet *k8sContext) applyContivNetworkPolicy(np *k8sNetworkPolicy) error {
	var err error
	//endPoint Group (EPG) aka group
	groupList := np.PodSelector.groupList
	//Walk for All Pod Selector Endpoint Group List
	for _, group := range groupList {
		//Check If; already exist or not if not create one
		if _, ok := k8sNet.epgName[group]; !ok {
			//Create EPG then
			npLog.Infof("EPG :%v doesn't exist create now!", group)
			if _, err = k8sNet.contivClient.EndpointGroupGet(
				np.PodSelector.TenantName, group); err != nil {
				npLog.Errorf("group %s failed in Contiv ", err)
				return err
			}
			k8sNet.epgName[group] = true
		}
		//Get PolicyMap per group
		policyMap := k8sNet.policyPerEpg[group]
		if _, ok := k8sNet.networkPolicy[np.PodSelector.PolicyName]; !ok {
			//Create Policy and published to ofnet controller
			if err = k8sNet.createPolicy(
				np.PodSelector.TenantName,
				np.PodSelector.PolicyName); err != nil {
				npLog.Errorf("Failed to create Policy :%v",
					np.PodSelector.PolicyName)
				return err
			}
			//Cache K8s Configure Policy
			k8sNet.networkPolicy[np.PodSelector.PolicyName] = np

			//Update this new Policy Instance in policyMap
			policyMap[np.PodSelector.PolicyName] = []string{}
			attachPolicy := []string{}

			//Walk all configured policy in policyMap table
			//and Build all configured policy so far added into
			//group
			for policyN := range policyMap {
				attachPolicy = append(attachPolicy, policyN)
			}
			//Call contiv Client API to update group with new
			//Policy
			if err = k8sNet.createEpg(
				np.PodSelector.NetworkName,
				np.PodSelector.GroupName, attachPolicy); err != nil {
				npLog.Errorf("failed to update EPG %s ", err)
				return err
			}
		} else {
			//XXX: Need check if policy rules are still same or not
			npLog.Warnf("Policy:%v already exist ",
				np.PodSelector.PolicyName)
		}

		//Start formulating Rules using Parse Policy Object
		rList := k8sNet.buildRuleFromIngressGroupSet(np,
			np.PodSelector.PolicyName)

		npLog.Infof("Build Rules Ingress Spec:%+v, rList:%+v",
			np.Ingress, rList)

		npLog.Infof("Pods Info in To Spec :%+v", np.PodSelector)
		toGroupSet := np.PodSelector.groupList

		//Build Final policy rule and pushed to Contiv system
		ruleList := k8sNet.finalGroupBaseNetworkPolicyRule(np, toGroupSet,
			rList, true)
		npLog.Infof("final rules :%v", ruleList)
	}
	return nil
}
func (k8sNet *k8sContext) appendPolicyPorts(rules *client.Rule, ports k8sPolicyPorts) {
	if len(ports.Protocol) > 0 {
		rules.Protocol = strings.ToLower(ports.Protocol)
	}
	if ports.Port != 0 {
		rules.Port = ports.Port
	}
	return
}
func (k8sNet *k8sContext) createUpdateRuleIds(rules *client.Rule) string {
	ruleID := rules.FromIpAddress + rules.ToIpAddress
	if rules.Port != 0 {
		ruleID += strconv.Itoa(rules.Port)
	}
	if len(rules.Protocol) > 0 {
		ruleID += rules.Protocol
	}
	rules.RuleID = ruleID
	return ruleID
}
func (k8sNet *k8sContext) delNetworkPolicy(np *network_v1.NetworkPolicy) {
	npLog.Infof("Delete network policy: %s", np.Name)
	policy, ok := k8sNet.networkPolicy[np.Name]
	if !ok {
		npLog.Errorf("network policy: %s doesn't exist", np.Name)
		return
	}

	if err := k8sNet.cleanupContivNetworkPolicy(policy); err != nil {
		npLog.Errorf("failed to delete network policy: %s, %v", np.Name, err)
		return
	}
	//Remove PolicyId from Policy Db
	delete(k8sNet.networkPolicy, np.Name)

	//Cleanup Per NameSpace nwPolicy obj
	if nwPolicyMap, ok := k8sNet.nwPolicyPerNameSpace[np.Namespace]; ok {
		delete(nwPolicyMap, np.Name)
	}
	npLog.Infof("Delete Policy:%s from contiv DB ", np.Name)
}

//Cleanup Configured k8s NetworkPolicy from contiv system
func (k8sNet *k8sContext) cleanupContivNetworkPolicy(np *k8sNetworkPolicy) error {
	var retErr error
	policyName := np.PodSelector.PolicyName
	groupList := np.PodSelector.groupList
	for _, epg := range groupList {
		policyMap, ok := k8sNet.policyPerEpg[epg]
		if !ok {
			npLog.Errorf("Failed to find epg Policy")
			return fmt.Errorf("failed to find epg ")
		}
		npLog.Infof("Cleanup policyName:%v PolicyPtr:%+v its rules", policyName,
			policyMap)
		for _, ruleID := range policyMap[policyName] { //Walk for all configured Rules
			npLog.Infof("Delete RulID:%v from policy:%v", ruleID, policyName)
			if err := k8sNet.deleteRule(np.PodSelector.TenantName, policyName, ruleID); err != nil {
				npLog.Warnf("failed to delete policy: %s rule: %s, %v",
					policyName, ruleID, err)
				retErr = err
			}
		}
		delete(policyMap, policyName)
		attachPolicy := []string{}
		for policyN := range policyMap {
			attachPolicy = append(attachPolicy, policyN)
		}
		//Unlink Policy From EPG
		if err := k8sNet.createEpg(np.PodSelector.NetworkName,
			epg, attachPolicy); err != nil {
			npLog.Errorf("failed to update EPG %s, %s", epg, err)
			retErr = err
		}
		//Delete Policy
		if err := k8sNet.deletePolicy(policyName); err != nil {
			npLog.Warnf("failed to delete policy: %s",
				np.PodSelector.TenantName)
			retErr = err
		}
	}
	npLog.Infof("Delete policy:%v ", policyName)
	return retErr
}

//Parse Ports information from Ingress Policy Port List
func (k8sNet *k8sContext) getPolicyPorts(
	policyPort []network_v1.NetworkPolicyPort) []k8sPolicyPorts {
	portList := []k8sPolicyPorts{}

	for _, pol := range policyPort {
		port := 0
		protocol := "TCP" // default

		if pol.Port != nil {
			port = pol.Port.IntValue()
		}
		if pol.Protocol != nil {
			protocol = string(*pol.Protocol)
		}
		npLog.Infof("ingress policy port: protocol: %v, port: %v",
			protocol, port)
		portList = append(rules,
			k8sPolicyPorts{Port: port,
				Protocol: protocol})
	}
	npLog.Infof("Total recv Parse Ports: %+v", portList)
	return portList
}

func (k8sNet *k8sContext) getIngressPolicyPeerInfo(
	peers []network_v1.NetworkPolicyPeer,
	nameSpace string) ([]*k8sNetworkPolicyPeer, error) {
	peerPolicy := []k8sNetworkPolicyPeer{}

	npLog.Infof("Ingress Policy Peer Info:%+v", peers)
	if len(peers) <= 0 {
		return peerPolicy, fmt.Errorf("empty pod selectors")
	}

	for _, from := range peers {
		//Podselector Case:
		if from.PodSelector != nil {
			//Get podSelector Group list
			s, err := k8sNet.parsePodSelectorForGroupSet(
				from.PodSelector.MatchLabels, nameSpace)
			if err != nil {
				return []*k8sPodSelector{}, err
			}
			npLog.Infof("Ingress policy pod-selector: %+v", s)
			peerPolicyInstance := k8sNetworkPolicyPeer{IngressPodSelector: s}
			peerPolicy = append(peerPolicy, peerPolicyInstance)
		} else if from.IPBlock != nil { //IP Block or CIDR case
			peerPolicyInstance := k8sNetworkPolicyPeer{IngressIpBlockSelector: from.IPBlock}
			peerPolicy = append(peerPolicy, peerPolicyInstance)
		} else if from.NamespaceSelector != nil {
			peerPolicyInstance := k8sNetworkPolicyPeer{IngressNameSelector: from.NamespaceSelector}
			peerPolicy = append(peerPolicy, peerPolicyInstance)

		} else {
			npLog.Errorf("Recv Invalid Ingress Policy info")
			break
		}
	}
	return &peerPolicy, nil
}

func (k8sNet *k8sContext) getIngressPodSelectorList(
	peers []network_v1.NetworkPolicyPeer,
	nameSpace string) ([]*k8sPodSelector, error) {
	peerPodSelector := []*k8sPodSelector{}

	npLog.Infof("Ingress Policy Peer Info:%+v", peers)

	if len(peers) <= 0 {
		return peerPodSelector, fmt.Errorf("empty pod selectors")
	}

	for _, from := range peers {
		//Currently Support for PodSelector
		if from.PodSelector != nil {
			s, err := k8sNet.parsePodSelectorInfo(
				from.PodSelector.MatchLabels, nameSpace)
			if err != nil {
				return []*k8sPodSelector{}, err
			}
			npLog.Infof("Ingress policy pod-selector: %+v", s)
			peerPodSelector = append(peerPodSelector, s)
		}
	}
	return peerPodSelector, nil
}

//Build Ingress Policy obj
func (k8sNet *k8sContext) parseIngressPolicy(
	npIngress []network_v1.NetworkPolicyIngressRule,
	nameSpace string) ([]k8sIngressRule, error) {

	ingressRules := []k8sIngressRule{}
	npLog.Infof("Recv Ingress Policy:=%+v", npIngress)
	if len(npIngress) <= 0 {
		return ingressRules, fmt.Errorf("no ingress rules")
	}
	//Walk in all received Ingress Policys
	for _, policy := range npIngress {
		//Parse Port Information from recv policy
		portList := k8sNet.getPolicyPorts(policy.Ports)
		//build Ingress Peer Obj
		fromPeer, err := k8sNet.
			getIngressPolicyPeerInfo(policy.From, nameSpace)
		if err != nil {
			return []k8sIngress{}, err
		}
		npLog.Infof("Policy PeerInfo:%+v", fromPeer)
		ingressRules = append(ingressRules,
			k8sIngressRule{Ports: portList,
				From: fromPeer})
	}
	return ingressRules, nil
}

func (k8sNet *k8sContext) getPodsIpsSetUsingLabel(m map[string]string,
	nameSpace string) ([]string, error) {
	var ipList []string
	// labels.Parser
	labelSectorStr := labels.SelectorFromSet(labels.Set(m)).String()
	//Quary to K8s Api server for pods of given Label selector
	podsList, err := k8sNet.k8sClientSet.CoreV1().
		Pods(nameSpace).
		List(meta_v1.ListOptions{LabelSelector: labelSectorStr})
	if err != nil {
		npLog.Fatalf("failed to get Pods from  K8S Server, %v", err)
		return nil, err
	}
	for _, pod := range podsList.Items {
		ipList = append(ipList, pod.Status.PodIP)
	}
	return ipList, nil
}
func (k8sNet *k8sContext) initPodSelectorCacheTbl(m map[string]string,
	podSelector *k8sPodSelector) error {
	if podSelector == nil {
		return fmt.Errorf("Passe Nil Pod Selector reference")
	}
	//XXX:Don't confused PodSelector with Pod: PodSelector object keeps all
	//attched label Ips
	if len(podSelector.podIps) <= 0 {
		podSelector.podIps = make(map[string]string, 0)
		npLog.Infof("Init PodSelector podIp table:%v",
			podSelector.labelPodMap)
	}
	//PodSelector: Keep track of all its label
	if len(podSelector.labelPodMap) <= 0 {
		podSelector.labelPodMap = make(map[string]map[string]bool, 0)
		for key, val := range m {
			lkey := getLabelSelector(key, val)
			podSelector.labelPodMap[lkey] = make(map[string]bool, 0)
		}
		npLog.Infof("Init PodSelector Map table:%v",
			podSelector.labelPodMap)
	}
	return nil
}

//Create podSelector object and Init its attributes i.e podIps , label etc
func (k8sNet *k8sContext) parsePodSelectorForGroupSet(
	labelMap map[string]string,
	nameSpace string) (*k8sPodSelector, error) {

	PodSelector := k8sPodSelector{
		TenantName:  nameSpace,
		NetworkName: defaultNetworkName,
		GroupName:   defaultEpgName}

	npLog.Infof("Build PodSelector Info using Label:%+v", m)
	for k, v := range labelMap {
		PodSelector.labelSelList = append(PodSelector.labelSelList,
			(k + v))
	}
	//Call Api to get EndGroupList where this Pod could be part of
	PodSelector.groupList = k8sNet.getGroupSetFromLabelSet(nameSpace,
		PodSelector.labelSelList)

	npLog.Info("PodSelector: %+v", PodSelector)
	return &PodSelector, nil
}

//Create podSelector object and Init its attributes i.e podIps , label etc
func (k8sNet *k8sContext) parsePodSelectorInfo(m map[string]string,
	nameSpace string) (*k8sPodSelector, error) {

	PodSelector := k8sPodSelector{
		TenantName:  nameSpace,
		NetworkName: defaultNetworkName,
		GroupName:   defaultEpgName}

	npLog.Infof("Build PodSelector Info using Label:%+v", m)

	labelSectorStr := labels.SelectorFromSet(labels.Set(m)).String()

	//Quary to K8s Api server using label Selector to get Pods list
	podsList, err := k8sNet.k8sClientSet.CoreV1().
		Pods(nameSpace).
		List(meta_v1.ListOptions{LabelSelector: labelSectorStr})
	if err != nil {
		npLog.Fatalf("failed to get Pods from  K8S Server, %v", err)
		return nil, err
	}
	if err = k8sNet.initPodSelectorCacheTbl(m, &PodSelector); err != nil {
		return nil, err
	}
	//Build mapping for Label To PodIP
	for _, pod := range podsList.Items {
		for key, val := range pod.ObjectMeta.Labels {
			lkey := getLabelSelector(key, val)
			npLog.Infof("Update label Selector key:%v", lkey)
			if ipMap, ok := PodSelector.labelPodMap[lkey]; ok {
				//Setup IpMap Tbl
				ipMap[pod.Status.PodIP] = true
			}
		}
	}
	//Recalculate Podselector Ips
	k8sNet.updatePodSelectorPodIps(&PodSelector)
	npLog.Info("PodSelector: %+v", PodSelector)
	return &PodSelector, err
}

//Update PodSelector Label IP mapping
func (k8sNet *k8sContext) updatePodSelectorLabelIPMap(
	podSelector *k8sPodSelector,
	labelSelector string,
	ipList []string,
	isAdd bool) {
	//Check Nil
	if podSelector == nil {
		npLog.Infof("Nil Pod Selector")
		return
	}
	if ipMap, ok := podSelector.labelPodMap[labelSelector]; ok {
		for _, ip := range ipList {
			if isAdd { //Add Pods
				ipMap[ip] = true
			} else { //remove Pods
				delete(ipMap, ip)
			}
		}
		npLog.Infof(" Pod Ips After Update Pod Selector:%+v event:%v",
			podSelector, isAdd)
	}
	return
}

//Create default deny rules for given policy
func (k8sNet *k8sContext) createPolicyWithDefaultRule(tenantName string,
	epgName, policyName string) error {
	var err error
	npLog.Infof("Create  default policy for epg:%s policy:%s",
		epgName, policyName)

	if err = k8sNet.createPolicy(defaultTenantName,
		epgName, policyName); err != nil {
		npLog.Infof("Failed to create Policy :%v", policyName)
		return err
	}

	//Add default rule into policy
	if err = k8sNet.createRule(&client.Rule{
		TenantName:        tenantName,
		PolicyName:        policyName,
		FromEndpointGroup: epgName,
		RuleID:            k8sutils.DenyAllRuleID + "in",
		Priority:          k8sutils.DenyAllPriority,
		Direction:         "in",
		Action:            "deny",
	}); err != nil {
		return err
	}
	policyMap := k8sNet.policyPerEpg[epgName]
	if len(policyMap) <= 0 {
		//Within Policy , Multiple Rules will be configured
		policyMap = make(map[string][]string, 0)
	}
	policyMap[policyName] = append(policyMap[policyName],
		(k8sutils.AllowAllRuleID + "in"))
	//k8sNet.policyRules[policyName] = append(k8sNet.policyRules[policyName],
	//	(k8sutils.AllowAllRuleID + "in"))
	return nil
}
func (k8sNet *k8sContext) createDefaultDenyRule(tenantName, epgName, policyName string) error {
	//Add default rule into policy
	if err := k8sNet.createRule(&client.Rule{
		TenantName:        tenantName,
		PolicyName:        policyName,
		FromEndpointGroup: epgName,
		RuleID:            k8sutils.DenyAllRuleID + "in",
		Priority:          k8sutils.DenyAllPriority,
		Direction:         "in",
		Action:            "deny",
	}); err != nil {
		return err
	}
	//k8sNet.policyRules[policyName] = append(k8sNet.policyRules[policyName],
	//	(k8sutils.DenyAllRuleID + "in"))
	policyMap := k8sNet.policyPerEpg[epgName]
	policyMap[policyName] = append(policyMap[policyName],
		(k8sutils.DenyAllRuleID + "in"))
	return nil
}

func (k8sNet *k8sContext) createDefaultPolicy(tenantName string,
	epgName string) error {
	return k8sNet.createPolicyWithDefaultRule(tenantName,
		epgName, defaultPolicyName)
}

//Create Tenant based on requested Tenant string
func (k8sNet *k8sContext) createTenant(tenant string) error {
	if err := k8sNet.contivClient.TenantPost(&contivClient.Tenant{
		TenantName: tenant,
	}); err != nil {
		npLog.Errorf("failed to create tenant: %s, %v", tenant, err)
		return err
	}
	npLog.Infof("Creating tenant: %s\n", tenant)
	return nil
}
