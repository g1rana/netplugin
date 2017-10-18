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
     1         2                         |  MemCached     |                                                                       ++
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


<h4>Packetflow</h4>

 * netplugin data path receives DNS msg from container
 * netplugin looks up the name in local cache
 * if there is any entry, DNS response is sent out
 * if there is no entry then the original DNS request is forwarded

```
                            +--------------------+
                            |DNS lookup:         |
                            |   #LB Service names|
                            |   #EPG names       |
                            |   #Container names |
                            +--------------------+
                          DNS ^  |DNS       |DNS
                          Req |  |Resp      |Fwd (lookup failed)
                              |  v          v
                            +-+------------------+
+-----------+      DNS Req  |                    |       DNS Fwd   +-------------+
|           |-------------->|     Netplugin      +---------------->|  External   |
|Container#1|               |     datapath       |                 |  DNS        |
|           |<--------------|                    |<----------------+             |
+-----------+ DNS Resp      +--------------------+       DNS Resp  +-------------+
```

