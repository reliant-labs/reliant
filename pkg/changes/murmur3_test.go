package changes

import "testing"

// TestMurmur3ReferenceVectors pins the hash to published MurmurHash3 x86_32
// answers, several at other seeds, so the implementation is checked against
// someone else's and not only against its web twin. Sources: the
// spaolacci/murmur3 reference table and Ian Boyd's test vectors for
// Appleby's SMHasher implementation.
func TestMurmur3ReferenceVectors(t *testing.T) {
	cases := []struct {
		seed uint32
		in   string
		want uint32
	}{
		{0, "", 0x00000000},
		{1, "", 0x514e28b7},
		{0xffffffff, "", 0x81f16f39},
		{0, "hello", 0x248bfa47},
		{1, "hello", 0xbb4abcad},
		{0x2a, "hello", 0xe2dbd2e1},
		{0, "hello, world", 0x149bbb7f},
		{0, "19 Jan 2038 at 3:14:07 AM", 0xe31e8a70},
		{0, "The quick brown fox jumps over the lazy dog.", 0xd5c48bfc},
		{0x2a, "The quick brown fox jumps over the lazy dog.", 0xc02d1434},
		// Every tail length, from Boyd's byte vectors.
		{0, "\x21\x43\x65\x87", 0xf55b516b},
		{0, "\x21\x43\x65", 0x7e4a8634},
		{0, "\x21\x43", 0xa0f7b07a},
		{0, "\x21", 0x72661cf4},
		{0, "\x00\x00\x00\x00", 0x2362f9de},
		{0, "\xff\xff\xff\xff", 0x76293b50},
		// UTF-8: "π" is two bytes, so eight of them are four whole blocks.
		{0x9747b28c, "ππππππππ", 0xd58063c1},
		{0x9747b28c, "Hello, world!", 0x24884cba},
		{0x9747b28c, "aaaa", 0x5a97808a},
	}
	for _, c := range cases {
		if got := murmur3([]byte(c.in), c.seed); got != c.want {
			t.Errorf("murmur3(%q, %#x) = %#08x, want %#08x", c.in, c.seed, got, c.want)
		}
	}
}
