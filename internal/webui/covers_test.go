package webui

import (
	"fmt"
	"testing"
)

func TestCoverCacheKeepsOnePicturePerSongWithinItsLimit(t *testing.T) {
	var c coverCache
	c.put("a", "1", []byte("old"))
	c.put("a", "2", []byte("newer"))
	if got := c.get("a"); got == nil || got.version != "2" || c.size != 5 || c.order.Len() != 1 {
		t.Fatalf("a song keeps its latest picture only: %+v size %d", got, c.size)
	}
	large := make([]byte, coverCacheLimit/4)
	for i := 0; i < 8; i++ {
		c.put(fmt.Sprint(i), "1", large)
		c.get("a")
	}
	if c.size > coverCacheLimit {
		t.Fatalf("over the limit: %d", c.size)
	}
	if c.get("a") == nil || c.get("0") != nil {
		t.Fatal("the picture used longest ago goes first")
	}
	c.put("huge", "1", make([]byte, coverCacheLimit+1))
	if c.get("huge") != nil {
		t.Fatal("a picture over the limit is not kept")
	}
	c.clear()
	if c.get("a") != nil || c.size != 0 {
		t.Fatal("clear left pictures behind")
	}
	c.put("a", "1", []byte("x"))
	if c.get("a") == nil {
		t.Fatal("unusable after clear")
	}
}
