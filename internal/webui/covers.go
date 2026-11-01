package webui

import (
	"container/list"
	"sync"
)

// The pictures kept for serving again. Most are a few dozen KB; one embedded in a local file can be many MB.
const coverCacheLimit = 32 << 20

type cachedCover struct {
	key     string // the song and scene
	version string // what the picture was read under: the record's revision and the address
	data    []byte
}

// coverCache keeps the picture last served for each song and scene, so that coming back to a song needs neither
// the extension nor the network. The ones used longest ago go first.
type coverCache struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order list.List
	size  int
}

func (c *coverCache) get(key string) *cachedCover {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.items[key]
	if element == nil {
		return nil
	}
	c.order.MoveToFront(element)
	return element.Value.(*cachedCover)
}

func (c *coverCache) put(key, version string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*list.Element{}
	}
	if element := c.items[key]; element != nil {
		c.remove(element)
	}
	if len(data) > coverCacheLimit {
		return
	}
	c.items[key] = c.order.PushFront(&cachedCover{key, version, data})
	c.size += len(data)
	for c.size > coverCacheLimit {
		c.remove(c.order.Back())
	}
}

func (c *coverCache) remove(element *list.Element) {
	item := c.order.Remove(element).(*cachedCover)
	delete(c.items, item.key)
	c.size -= len(item.data)
}

func (c *coverCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items, c.size = nil, 0
	c.order.Init()
}
