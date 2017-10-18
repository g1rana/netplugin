<h1>NetworkPolicy (K8s networkPolicy support)</h1>

* Contiv plugin will support native K8s network policy in its dataPath
* There would be no explicit contiv specific configurations required to support above policy  

Following are components which required to implement this feature : 
 
 * Policy Watcher (new component in NetMaster)
 * Policy Memcached DB for PolicyID
 
<h4>Software Arch</h4>

```
  +--------------------+
  |                    |
  | K8s API Server     |
  |                    |
  +----+---------+-----+
       |         ^                  +------------------------+
       +         +                  | Pods Events            |
       1         2        +1+-------+ Create/Update/Delete   |
       +         +        |         +------------------------+
       |         |        v
+------+---------+--------++
|     NetMaster Watcher    |
|     1. Policy Events     |
|     2. Pods Events       |                +-- -------------+
|                          +-------3--------+   Configured   |
+---------------------+----+                |   Policy DB    |
           5          |                     |                |
           +          5                     +----------------+
           |          |
 +---------+----------v-------+
 |     Ovs DataPath           |
 |     Policy based ACL       |
 |                            |
 |                            |
 +----------------------------+


```

There are 2 types of work flow has been identified : 
1. Policy Event  from API server 

2. Pod Event trigger policy update 

Contiv is using existing  policy based infrasturture to support K8s based network policy.With this approach, Contiv will create a default Policy group implicitly on creation of network and used or implement K8s network policy using EPG. There is no need to specifiy any contiv specific tag in K8s deployment/resource yml. Contiv will mapped network namespace to network.K8s network policy will mapped into  OVS ACL i.e IP to IP or IP to network or network  to network.

Here are following workflow describe as :
<h4>Work Flow 1: </h4>
<h4>Policy create event :</h4> 

```
1. Parse received Policy create event
2. Get all Pods belongs to source Label 
3. Get all Pods belongs to dest Label 
4. Build Policy rules and updates into policy configuration DB
5. Trigger OVS update for requested policy .

```
   
 <h4>Policy update event :</h4>   

```
1. Parse received event 
2. Update Label memcached if required.
3. Update Policy DB if required
4. Trigger Policy update to OVS

```
  <h4>Policy delete event :</h4>

```
1. Parse received Policy event.
2. Update memcached DB with Policy labels.
3. Delete Policy from Policy DB
4. Delete Policy from OVS datapath

```
<h4>Work Flow 2: </h4> 

```
<h4>Newly Created Pod Added into existing Policy:</h4>
1. Netmaster Listen to Pod events 
2. Request APIs server to Pods Label
3. Query Policy wrt Pod Label from API server
4. Update Config Policy DB
5. Trigger OVS update 

```
<h4>Work Flow 2: </h4> 

```
<h4>Newly Created Pod delete into existing Policy:</h4>
1. Netmaster Listen to Pod events 
2. Request APIs server to Pods Label
3. Query Policy wrt Pod Label from API server
4. Update Config Policy DB 
5. Trigger OVS update 

```
       
