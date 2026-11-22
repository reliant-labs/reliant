package changes

import (
	"encoding/binary"
	"math/bits"
)

// murmur3 is MurmurHash3 x86_32 (Austin Appleby's reference, public domain)
// over data with the given seed. The spec fixes the seed at 0; the parameter
// exists so the published reference vectors, several of which use other
// seeds, can pin this implementation.
func murmur3(data []byte, seed uint32) uint32 {
	const (
		c1 = 0xcc9e2d51
		c2 = 0x1b873593
	)
	h := seed
	n := len(data)
	i := 0
	for ; i+4 <= n; i += 4 {
		k := binary.LittleEndian.Uint32(data[i:])
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
		h = bits.RotateLeft32(h, 13)
		h = h*5 + 0xe6546b64
	}
	var k uint32
	switch n & 3 {
	case 3:
		k ^= uint32(data[i+2]) << 16
		fallthrough
	case 2:
		k ^= uint32(data[i+1]) << 8
		fallthrough
	case 1:
		k ^= uint32(data[i])
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
	}
	h ^= uint32(n)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// bucket maps s to 0..9999: murmur3 of its UTF-8 bytes, seed 0, mod 10000.
func bucket(s string) int {
	return int(murmur3([]byte(s), 0) % 10000)
}
