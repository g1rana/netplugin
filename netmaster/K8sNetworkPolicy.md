
Contiv Network Policy for native Kubernetes Network policy : 
Current verison of Contiv network policy used its configurtion interface framework to provide policy based networking for container.
This policy configuration is required its own configuration attribute to provide policy based networking. Latest version of kubernetes 
has provide configuration interface to configure network policy for kubernetes manages PODs. The main configuration attribute for kubernetes
network policy are name space and label and mode (ingress,egress or both). To configure these network policy in Kubernetes
cluster,configured network plugin should implemented these policy.Kubernetes network policy would be no-op for the network
plugin who doesn’t implement .Contiv as network netplgin is planing to support Kubernetes policy.
Contiv Kubernetes Policy : 
Current Contiv do support network policy but it’s own configuration way. Current approach won’t intergated direcetly to
Kubernetes way of configurtion. To support Kubernetes network policy we are thinking to following design aspect :
Assumption :
* * 1. Very minimal changes into policy configuration in contiv data-path
* 2.t y Try to use existing backend code to configure contiv data-path
Current design approach: 
Contiv would subscribe network-policy resource with K8s API server and start to building up its own configure 
database. This new database would contain all running or applyied K8s network policy. Contiv will use this new
configured database and converts into existing newtwork poilcy API to configure its data path.
mermaid
graph LR
A[Hard edge] —>B(Round edge)
    B —> C{Decision}
    C —>|One| D[Result one]
    C —>|Two| E[Result two]
​
        
        
        
