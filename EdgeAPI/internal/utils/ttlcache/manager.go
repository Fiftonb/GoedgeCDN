// Copyright 2021 GoEdge CDN goedge.cdn@gmail.com. All rights reserved.

package ttlcache

import (
	"sync"
	"time"

	"github.com/TeaOSLab/EdgeAPI/internal/goman"
	"github.com/TeaOSLab/EdgeAPI/internal/zero"
)

var SharedManager = NewManager()

type Manager struct {
	ticker *time.Ticker
	locker sync.Mutex

	cacheMap map[*Cache]zero.Zero
}

func NewManager() *Manager {
	var manager = &Manager{
		ticker:   time.NewTicker(2 * time.Second),
		cacheMap: map[*Cache]zero.Zero{},
	}

	goman.New(func() {
		manager.init()
	})

	return manager
}

func (this *Manager) init() {
	for range this.ticker.C {
		this.locker.Lock()
		for cache := range this.cacheMap {
			cache.GC()
		}
		this.locker.Unlock()
	}
}

func (this *Manager) Add(cache *Cache) {
	this.locker.Lock()
	this.cacheMap[cache] = zero.New()
	this.locker.Unlock()
}

func (this *Manager) Remove(cache *Cache) {
	this.locker.Lock()
	delete(this.cacheMap, cache)
	this.locker.Unlock()
}

func (this *Manager) Count() int {
	this.locker.Lock()
	defer this.locker.Unlock()
	return len(this.cacheMap)
}
