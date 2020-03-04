package rtutil

import (
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/minio/blake2b-simd"
)

const (
	ADDR_LEN = 32
)

//Dist calculating distance for two address,  only first 64bit is calcucated
//slow version to calculating distance
func Dist(p1, p2 peer.ID) int {
	numbytes1 := []byte(p1)[2:10]
	numbytes2 := []byte(p2)[2:10]

	leadZero := 0
	bytes := ADDR_LEN / 8
	for i := 0; i < bytes; i++ {
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
	return leadZero
}
func Hash(b []byte) []byte {
	s := blake2b.Sum256(b)
	return s[:]
}
