package rtutil

import (
	"github.com/libp2p/go-libp2p-core/peer"
)

//Dist calculating distance for two address,  only first 64bit is calcucated
//slow version to calculating distance
func Dist(p1, p2 peer.ID) int {
	numbytes1 := []byte(p1)[2:10]
	numbytes2 := []byte(p2)[2:10]

	leadZero := 0
	for i := 0; i < 8; i++ {
		res := numbytes1[i] ^ numbytes2[i]
		if res == 0 {
			leadZero += 8
		} else {
			for j := 7; j >= 0; j-- {
				if ((1 << j) & res) != 0 {
					break
				} else {
					leadZero++
				}
			}
			break
		}
	}
	return 64 - leadZero
}
