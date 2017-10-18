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
