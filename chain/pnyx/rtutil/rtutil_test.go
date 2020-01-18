package rtutil

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"
)

func Test_Dist(t *testing.T) {

	addr1, _ := hex.DecodeString("0001020304050107")
	addr2, _ := hex.DecodeString("00010203040506F0")
	addr3, _ := hex.DecodeString("0001020304050F00")
	addr4, _ := hex.DecodeString("000102030F000000")
	addr5, _ := hex.DecodeString("0001020330000000")
	addr6, _ := hex.DecodeString("0001020200000000")
	addr7, _ := hex.DecodeString("000102F000000000")
	addr8, _ := hex.DecodeString("00010F00001234a7")
	addr9, _ := hex.DecodeString("0001F09890809800")
	addr10, _ := hex.DecodeString("0FFF000000000000")
	results := []int{}
	//should := []int{0, 11,12,28,30,33,40,44,48,60}
	addrs := []string{string(addr1), string(addr2), string(addr3), string(addr4), string(addr5), string(addr6), string(addr7), string(addr8), string(addr9), string(addr10)}
	for index, addr := range addrs {
		dist := Dist(peer.ID(addr1), peer.ID(addr))
		results = append(results, dist)
		t.Logf(fmt.Sprintf("dist of %v is : %v , %v", index, dist, 64-dist))

	}
	fmt.Println(results)
}
