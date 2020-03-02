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
}

func CreateRouter() (*RouteInfo, error) {
	cache := lru.New(maxRouteItems).LRU().Build()
	neighbours := lru.New(maxNeighbours).LRU().Build()
	return &RouteInfo{
		table:          cache,
		liveNeighbours: neighbours,
	}, nil
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

func (ri *RouteInfo) UpdateNeighbour(next peer.ID, breakdown bool) {
	ri.mutex.Lock()
	defer ri.mutex.Unlock()
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
	ri.UpdateNeighbour(next, false)
	items, ok := ri.table.Get(dest)
	if ok != nil {
		newItems := append([]*RouteTableItem{}, &RouteTableItem{next, ttl})
		ri.table.SetWithExpire(dest, newItems, expireTime)
	} else {
		target := items.([]*RouteTableItem)
		//大于等于3个了，找到相同的或是最差的一个换，最差的一个是指（按顺序）
		//1. 已经断线的 2. 超时的   3. ttl最大的
		sameIndex := 255
		brokenIndex := 255

		maxTTLIndex := 255
		maxTTL := uint8(0)
		toCheck := len(target)
		if toCheck >= int(alpha) {
			toCheck = int(alpha)
		}
		for index, item := range target[:toCheck] {
			if item.next == next {
				sameIndex = index
				break
			} else if !ri.liveNeighbours.Has(item.next) {
				brokenIndex = index
			}
			if item.ttl > maxTTL {
				maxTTL = item.ttl
				maxTTLIndex = index
			}
		}
		toModify := 255
		if sameIndex < 3 {
			toModify = sameIndex
		} else if brokenIndex < 3 {
			toModify = brokenIndex
		} else if maxTTLIndex < 3 {
			toModify = maxTTLIndex
		}
		if toModify < 3 {
			item := target[toModify]
			item.next = next
			//only need update if ttl is changed
			if item.ttl != ttl {
				item.ttl = ttl
				ri.table.SetWithExpire(dest, target, expireTime)
			}

		}
	}
}
