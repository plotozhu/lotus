package transp2p

import (
	"sync"
	"time"

	lru "github.com/bluele/gcache"
	peer "github.com/libp2p/go-libp2p-peer"
	"golang.org/x/xerrors"
)

const (
	alpha          uint8 = 2
	maxRouteItems  int   = 1024 * 128
	expireTime           = time.Hour
	fastExpireTime       = 10 * time.Minute
)

type RouteTableItem struct {
	next peer.ID
	ttl  uint8
}
type RouteInfo struct {
	// {dest <-> RouteTableItem信息}
	table lru.Cache
	// live neighbour info
	liveNeighbours lru.Cache
	mutex          sync.Mutex
	obsLock        sync.Mutex
	observers      []RouteObserver
}

//RouteObserver observer interface for RouteItem
type RouteObserver interface {
	//RouteItemUpdated is called when any route item is changed, routeitem is deleted when items is nil
	RouteItemUpdated(dst peer.ID, items []*RouteTableItem)
}

//CreateRouter create new route table
func CreateRouter() (*RouteInfo, error) {
	cache := lru.New(maxRouteItems).LRU().Build()
	neighbours := lru.New(maxNeighbours).LRU().Build()
	observers := make([]RouteObserver, 0)
	return &RouteInfo{
		table:          cache,
		liveNeighbours: neighbours,
		observers:      observers,
	}, nil
}

//Attach add new observer to watch changes of routeitem
func (ri *RouteInfo) Attach(observer RouteObserver) {
	ri.obsLock.Lock()
	defer ri.obsLock.Unlock()
	for _, obs := range ri.observers {
		if obs == observer {
			return
		}
	}
	ri.observers = append(ri.observers, observer)
}

//Dettach delete observe from watchlist
func (ri *RouteInfo) Dettach(observer RouteObserver) {
	ri.obsLock.Lock()
	defer ri.obsLock.Unlock()
	for index, obs := range ri.observers {
		if obs == observer {
			if index == len(ri.observers)-1 {
				ri.observers = ri.observers[:index]
			} else if index == 0 {
				ri.observers = ri.observers[1:index]
			} else {
				ri.observers = append(ri.observers[0:index], ri.observers[index+1:]...)
			}
		}
	}

}

//GetRoutes return all route items for dest, including those older than 1 hour, it will return nil,error where there is no route items
func (ri *RouteInfo) GetRoutes(dest peer.ID) ([]*RouteTableItem, error) {
	ri.mutex.Lock()
	defer ri.mutex.Unlock()
	items, ok := ri.table.Get(dest)
	if ok != nil {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	//filter unconnected neighbours
	retItems := make([]*RouteTableItem, 0)
	for _, item := range items.([]*RouteTableItem) {
		if ri.liveNeighbours.Has(item.next) {
			retItems = append(retItems, item)
		}
	}
	if len(retItems) == 0 {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	return retItems, nil
}

// GetBestRoute returns best router item for dest, it return error when there are no route item or all items are longer than 1 hour
func (ri *RouteInfo) GetBestRoute(dest peer.ID) (*RouteTableItem, error) {
	ri.mutex.Lock()
	defer ri.mutex.Unlock()
	rawItems, ok := ri.table.Get(dest)
	if ok != nil {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	items := rawItems.([]*RouteTableItem)
	maxttl := uint8(255)
	index := uint8(255)

	for i, item := range items {
		if item.ttl < maxttl && ri.liveNeighbours.Has(item.next) {
			maxttl = item.ttl
			index = uint8(i)
		}
	}
	if index == 255 {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	return items[index], nil
}

//UpdateNeighbour is called to reset timer of each neighbour
// for efficiency, breakdown will not cause route item change immediately, route item will be filtered on GetRouteItem
func (ri *RouteInfo) UpdateNeighbour(next peer.ID, breakdown bool) {
	ri.mutex.Lock()
	defer ri.mutex.Unlock()
	ri.updateNeighbour(next, breakdown)

}

//UpdateNeighbour is called to reset timer of each neighbour
// for efficiency, breakdown will not cause route item change immediately, route item will be filtered on GetRouteItem
func (ri *RouteInfo) updateNeighbour(next peer.ID, breakdown bool) {

	if breakdown {
		ri.liveNeighbours.Remove(next)
	} else {
		ri.liveNeighbours.SetWithExpire(next, true, 3*time.Minute)
	}
}

//UpdateRoute will update route item for dest, update will occurs only when
// 1. no more than 3 route items for dest, or
// 2. next is not exist  or ttl is smaller
// 3. there are items older than 1 hour
func (ri *RouteInfo) UpdateRoute(dest, next peer.ID, ttl uint8) {

	ri.mutex.Lock()
	defer ri.mutex.Unlock()
	ri.updateNeighbour(next, false)
	items, ok := ri.table.Get(dest)
	var changedItem []*RouteTableItem = nil
	if ok != nil {
		newItems := append([]*RouteTableItem{}, &RouteTableItem{next, ttl})
		ri.table.SetWithExpire(dest, newItems, expireTime)
		changedItem = newItems
	} else {
		target := items.([]*RouteTableItem)
		//大于等于3个了，找到相同的或是最差的一个换，最差的一个是指（按顺序）
		//1. 已经断线的 2. 超时的   3. ttl最大的
		sameIndex := uint8(255)
		brokenIndex := uint8(255)

		maxTTLIndex := uint8(255)
		maxTTL := uint8(0)
		toCheck := len(target)
		if toCheck >= int(alpha) {
			toCheck = int(alpha)
		}
		//find same one to replace
		for index, item := range target[:toCheck] {
			if item.next == next {
				sameIndex = uint8(index)
				break
			} else if !ri.liveNeighbours.Has(item.next) {
				brokenIndex = uint8(index)
			}
			if item.ttl > maxTTL {
				maxTTL = item.ttl
				maxTTLIndex = uint8(index)
			}
		}
		toModify := false
		/*fmt.Printf("\nChange route %v, from {\t", dest)
		for _, info := range target {
			fmt.Printf("%v:%v,", info.next.Pretty(), info.ttl)
		}
		fmt.Printf("}")*/
		if sameIndex < alpha {
			if target[sameIndex].ttl > ttl {
				target[sameIndex].ttl = ttl
				toModify = true
			}
		} else if brokenIndex < alpha {
			target[brokenIndex].ttl = ttl
			target[brokenIndex].next = next
			toModify = true
		} else if len(target) < int(alpha) {
			target = append(target, &RouteTableItem{next: next, ttl: ttl})
			toModify = true
		} else if maxTTLIndex < alpha {
			target[maxTTLIndex].ttl = ttl
			target[maxTTLIndex].next = next
			toModify = true
		}
		if toModify {
			ri.table.SetWithExpire(dest, target, expireTime)
			changedItem = target

			/*	fmt.Printf("after:{")
				for _, info := range target {
					fmt.Printf("%v:%v,", info.next.Pretty(), info.ttl)
				}
				fmt.Printf("}\n")
			*/
		} else {
			//fmt.Print("remain unchanged\n")
		}
	}
	//nodify watcher if changed
	if changedItem != nil && len(ri.observers) > 0 {
		ri.obsLock.Lock()
		defer ri.obsLock.Unlock()
		for _, observer := range ri.observers {
			go func(observer RouteObserver, changedItem []*RouteTableItem, dst peer.ID) {
				observer.RouteItemUpdated(dst, changedItem)
			}(observer, changedItem, dest)
		}
	}
}
