<h1>NetworkPolicy (K8s networkPolicy support)</h1>

* Contiv plugin will support native K8s network policy in its dataPath
* There would be no explicit contiv specific configurations required to support above policy  

Following are components which required to implement this feature : 
 
 * Policy Watcher (new component in NetMaster)
 * Memcached DB for Labels to IPs  
 * Policy Memcached DB for PolicyID
 
<h4>Software Arch</h4>

```

+--------------------+
|                    |
|     K8s Api Server |
|                    |
+----+---------+-----+
     |         ^
     |         |                         +----------------+
     1         2                         |  MemCached     |
     |         |        +--------3------->  (Label to Ips)|
     |         |        |                |                |
+----v---------+--------+                +----------------+
| Netmaster             |
| (Policy Watcher)      +----------4----------+
+--------+--------------+                     |
         |                                  +-v---------------+
         |                                  |                 |
         5                                  |Configured Policy
         |                                  |DataBase         |
         |                                  |                 |
 +-------v----------------+                 |                 |
 |                        |                 +-----------------+
 |OVS DataPath            |
 |(Insert Policy Rules)   |
 |                        |
 +------------------------+

```

With above design approach, Netmaster(Policy Watcher) listen for policy events and builds its KV database to map configured policy into OVS dataPath. 

<h4>Policy create event :</h4> 

```
1. Parse received Policy create event
2. Get all Pods belongs to source Label and update memcached DB
3. Get all Pods belongs to dest Label and update memcached DB
4. Allocate/update policy DB entry with received Policy configuration
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
       
