package transp2p

import (
	"sync"
	"time"

	"github.com/golang/groupcache/lru"
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
	next     peer.ID
	ttl      uint8
	lastSeen time.Time
}
type RouteInfo struct {
	table *lru.Cache
	mutex sync.Mutex
}

func CreateRouter() (*RouteInfo, error) {
	cache := lru.New(maxRouteItems)
	return &RouteInfo{
		table: cache,
	}, nil
}

//GetRoutes return all route items for dest, including those older than 1 hour, it will return nil,error where there is no route items
func (ri *RouteInfo) GetRoutes(dest peer.ID) ([]*RouteTableItem, error) {
	ri.mutex.Lock()
	ri.mutex.Unlock()
	items, ok := ri.table.Get(dest)
	if !ok {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	return items.([]*RouteTableItem), nil
}

// GetBestRoute returns best router item for dest, it return error when there are no route item or all items are longer than 1 hour
func (ri *RouteInfo) GetBestRoute(dest peer.ID) (*RouteTableItem, error) {
	ri.mutex.Lock()
	ri.mutex.Unlock()
	rawItems, ok := ri.table.Get(dest)
	if !ok {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	items := rawItems.([]*RouteTableItem)
	maxttl := uint8(255)
	index := uint8(255)
	for i, item := range items {
		if item.ttl < maxttl && time.Since(item.lastSeen) < expireTime {
			maxttl = item.ttl
			index = uint8(i)
		}
	}
	if index == 255 {
		return nil, xerrors.Errorf("No route item found for %v", dest)
	}
	return items[index], nil
}

//UpdateRoute will update route item for dest, update will occurs only when
// 1. no more than 3 route items for dest, or
// 2. next is exist and lastseen is older than fastExpireTime or ttl is smaller
// 3. there are items older than 1 hour
func (ri *RouteInfo) UpdateRoute(dest, next peer.ID, ttl uint8) {
	ri.mutex.Lock()
	ri.mutex.Unlock()
	items, ok := ri.table.Get(dest)
	if !ok {
		newItems := append([]*RouteTableItem{}, &RouteTableItem{next, ttl, time.Now()})
		ri.table.Add(dest, newItems)
	} else {
		target := items.([]*RouteTableItem)
		if len(target) < int(alpha) {
			target := append(target, &RouteTableItem{next, ttl, time.Now()})
			ri.table.Add(dest, target)
		} else {
			//大于等于3个了，找到最差的一个换
			updated := false
			modified := false
			for _, item := range target[:3] {
				if item.next == next {
					if time.Since(item.lastSeen) > fastExpireTime || ttl <= item.ttl {
						item.ttl = ttl
						item.lastSeen = time.Now()
						modified = true
					}
					updated = true
					break
				}
			}
			if !updated {
				indexOldest := -1
				oldest := int64(0)
				//find a most oldest one and replace it
				for index, item := range target[:3] {
					elasped := int64(time.Since(item.lastSeen))
					if elasped > oldest {
						indexOldest = index
						oldest = elasped

					}
				}
				if indexOldest >= 0 {
					item := target[indexOldest]
					if time.Since(item.lastSeen) > expireTime {
						item.next = next
						item.ttl = ttl
						item.lastSeen = time.Now()
						modified = true
					}
				}

			}
			if modified {
				ri.table.Add(dest, target)
			}
		}
	}
}
