package freeproxy

import (
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"
	"github.com/soluchok/freeproxy/providers"
)

var (
	instance  *ProxyGenerator
	usedProxy sync.Map
	once      sync.Once
)

type Verify func(proxy string) bool

type ProxyGenerator struct {
	lastValidProxy string
	cache          *cache.Cache
	VerifyFn       Verify
	canLoad        uint32
	providers      []Provider
	proxy          chan string
	job            chan string
}

func (p *ProxyGenerator) isProvider(provider Provider) bool {
	for _, pr := range p.providers {
		if reflect.TypeOf(pr) == reflect.TypeOf(provider) {
			return true
		}
	}
	return false
}

func (p *ProxyGenerator) AddProvider(provider Provider) {
	if !p.isProvider(provider) {
		p.providers = append(p.providers, provider)
	}
}

func (p *ProxyGenerator) load() {
	for _, provider := range p.providers {
		usedProxy.Store(p.lastValidProxy, time.Now().Hour())
		provider.SetProxy(p.lastValidProxy)

		ips, err := provider.List()
		if err != nil {
			logrus.Errorf("cannot load list of proxy %s err:%s", provider.Name(), err)
			continue
		}

		usedProxy.Range(func(key, value interface{}) bool {
			if value.(int) != time.Now().Hour() {
				usedProxy.Delete(key)
			}
			return true
		})

		logrus.Debugf("provider %s found ips %d", provider.Name(), len(ips))
		for _, proxy := range ips {
			p.job <- proxy
		}
	}
	atomic.StoreUint32(&p.canLoad, 0)
	return
}

func (p *ProxyGenerator) Get() string {
	select {
	case proxy := <-p.proxy:
		_, ok := usedProxy.Load(proxy)
		if !ok {
			p.lastValidProxy = proxy
		}
		return proxy
	case <-time.After(time.Millisecond * 500):
		if atomic.LoadUint32(&p.canLoad) == 0 {
			atomic.StoreUint32(&p.canLoad, 1)
			go p.load()
		}
	}
	return p.Get()
}

func (p *ProxyGenerator) verifyWithCache(proxy string) bool {
	val, found := p.cache.Get(proxy)
	if found {
		return val.(bool)
	}
	res := p.VerifyFn(proxy)
	p.cache.Set(proxy, res, cache.DefaultExpiration)
	return res
}

func (p *ProxyGenerator) do(proxy string) {
	if p.verifyWithCache(proxy) {
		p.proxy <- proxy
	}
}

func (p *ProxyGenerator) worker() {
	for proxy := range p.job {
		p.do(proxy)
	}
}

func (p *ProxyGenerator) run() {
	for w := 1; w <= 40; w++ {
		go p.worker()
	}
}

func New() *ProxyGenerator {
	once.Do(func() {
		instance = &ProxyGenerator{
			cache:    cache.New(10*time.Minute, 15*time.Minute),
			VerifyFn: verifyProxy,
			proxy:    make(chan string),
			job:      make(chan string),
		}

		//add providers to generator
		instance.AddProvider(providers.NewHidemyName())
		instance.AddProvider(providers.NewFreeProxyList())
		instance.AddProvider(providers.NewXseoIn())
		instance.AddProvider(providers.NewFreeProxyListNet())
		instance.AddProvider(providers.NewCoolProxy())
		instance.AddProvider(providers.NewProxyTech())
		//run workers
		go instance.run()
	})
	return instance
}
