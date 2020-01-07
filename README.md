


# Pnyx Consensus  

This is implementation and evaluation for Pnyx [Pnyx spec]().

### Description

Shard system is most promising sheme for scale-out blockchain, and there are six nessary conditions for shard system:
1. Random block proposal
2. Fair incentive system
3. Low communication cost
4. Liveness in asynchronzied network
5. Fast finalization
6. Safety for shards

There is no algorithm can provide all these features, and we present new consensus algorithm -- Pnyx Consensus.


## Development

This implementation is based on [lotus](https//githbu.com/filecoin-project/lotus) from filecoin project.

## Building from source
TBD
### Docker

## Evaluation 
Evaluation performance and rubost with multiple and public network simulation. We have tested all these in 1024 nodes, the results were illustrated in [paper]()


### Building evaluation image

### Phase 1
* Average block propagation time in different bandwidth and blocksize.

* average propagation time for 95% nodes 



### Phase 2
preset: 
1. blocksize: 256KB
2. bandwidth: 20Mbps
3. delay :150ms of one hop

evaluating:
failure percent for block sync when  transmit error range fromm 0% to  20% of on hop


### Phase 3
preset: 
1. blocksize: 256KB
2. bandwidth: 20Mbps
3. delay :150ms of one hop

evaluating:

> failure percent for block proposal  when  packet loss ranges fromm 0% to  20% of on hop

### Phase 4

preset: 
1. blocksize: 256KB
2. bandwidth: 20Mbps
3. delay :150ms of one hop

evaluating:

>failure percent for vote  when  packet loss ranges fromm 0% to  20% of on hop

### Phase 5

preset: 
1. blocksize: 256KB
2. bandwidth: 20Mbps
3. delay : 150ms of one hop

evaluating:

>failure percent for vote  when  packet loss ranges fromm 0% to  20% of on hop

**note: Modifiy parameter to evaluation more schema of Phase 2-5**

### Phase 6

preset: 
1. blocksize: 256KB
2. bandwidth: 20Mbps
3. delay : 150ms of one hop
4. packet loss: 1 percent%

evaluating:

>block generation  and finalization time  when  malice node percent ranges from 0% to  49% in commitee




## 授权信息
### Source code 
Dual-licensed under [MIT](https://github.com/filecoin-project/lotus/blob/master/LICENSE-MIT) + [Apache 2.0](https://github.com/filecoin-project/lotus/blob/master/LICENSE-APACHE)

### 
Pnyx algorithm are patented, anyone who use this source code or other implentations should gain licensed
