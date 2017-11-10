package k8sutils

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"

	log "github.com/Sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	k8sRest "k8s.io/client-go/rest"
)

// ContivConfig holds information passed via config file during cluster set up
type ContivConfig struct {
	K8sAPIServer string `json:"K8S_API_SERVER,omitempty"`
	K8sCa        string `json:"K8S_CA,omitempty"`
	K8sKey       string `json:"K8S_KEY,omitempty"`
	K8sCert      string `json:"K8S_CERT,omitempty"`
	K8sToken     string `json:"K8S_TOKEN,omitempty"`
	SvcSubnet    string `json:"SVC_SUBNET,omitempty"`
}

// contivKubeCfgFile holds credentials to access k8s api server
const (
	contivKubeCfgFile = "/opt/contiv/config/contiv.json"
	defSvcSubnet      = "10.254.0.0/16"
	tokenFile         = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	// DenyAllRuleID default deny all rule id
	DenyAllRuleID = "deny-all-0-"
	// DenyAllPriority default deny all rule priority
	DenyAllPriority = 1
	// AllowAllRuleID default allow all rule id
	AllowAllRuleID = "allow-all-0-"
	// AllowAllPriority default deny all rule priority
	AllowAllPriority = 1

	// K8sTenantLabel  k8s tenant label used by contiv
	K8sTenantLabel = "io.contiv.tenant"
	// K8sNetworkLabel k8s network label used by contiv
	K8sNetworkLabel = "io.contiv.network"
	// K8sGroupLabel k8s group label used by contiv
	K8sGroupLabel = "io.contiv.net-group"
)

// EpgNameToPolicy generate policy name from endpoint group
func EpgNameToPolicy(epgName, policyName string) string {
	return epgName + "-" + policyName + "-policy"
}

// PolicyToRuleID generate rule id from policy details
func PolicyToRuleID(epgName string, protocol string, port int, direction string) string {
	return epgName + "-" + protocol + "-" + strconv.Itoa(port) + "-" + direction
}

// PolicyToRuleID generate rule id from policy details
func PolicyToRuleIDUsingIps(InIps, FromIps string, port int, protocol, policyName string) string {
	//return InIps + "-" + FromIps + "-" + strconv.Itoa(port) + "-" + protocol
	return policyName + "-" + InIps + "-" + FromIps
}

// GetK8SConfig reads and parses the contivKubeCfgFile
func GetK8SConfig(pCfg *ContivConfig) error {
	bytes, err := ioutil.ReadFile(contivKubeCfgFile)
	if err != nil {
		return err
	}

	pCfg.SvcSubnet = defSvcSubnet
	err = json.Unmarshal(bytes, pCfg)
	if err != nil {
		return fmt.Errorf("Error parsing config file: %s", err)
	}

	// If no client certs or token is specified, get the default token
	if len(strings.TrimSpace(pCfg.K8sCert)) == 0 && len(strings.TrimSpace(pCfg.K8sToken)) == 0 {
		pCfg.K8sToken, err = getDefaultToken()
		if err != nil {
			log.Errorf("Failed: %v", err)
			return err
		}
	}

	return nil
}

// SetUpK8SClient init K8S client
func SetUpK8SClient() (*kubernetes.Clientset, error) {
	var contivK8sCfg ContivConfig
	err := GetK8SConfig(&contivK8sCfg)
	if err != nil {
		log.Errorf("Failed to get K8S config: %v", err)
		return nil, err
	}

	// init k8s client
	restCfg := &k8sRest.Config{
		Host:            contivK8sCfg.K8sAPIServer,
		BearerToken:     contivK8sCfg.K8sToken,
		TLSClientConfig: k8sRest.TLSClientConfig{CAFile: contivK8sCfg.K8sCa},
	}
	clientSet, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Errorf("failed to create kubernetes client instance %s, %+v", err, restCfg)
		return nil, err
	}

	return clientSet, nil
}

// getDefaultToken gets the token to access kubernetes API Server
// from the secrets loaded on the container
func getDefaultToken() (string, error) {
	bytes, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		log.Errorf("Failed: %v", err)
		return "", err
	}
	return string(bytes), nil
}
