package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	maxHttpRequestSize      = 8 * 1024
	contentType             = "Content-Type"
	respMimeApplicationJson = "application/json; charset=UTF-8"
)

func fatal(msg string, err error) {
	fmt.Printf("Fatal error %s: %v\n", msg, err)
	os.Exit(1)
}

/**
 * The cache
 */

type cache struct {
	lock sync.RWMutex
	m    map[string]*cacheEntry1
}

type cacheEntry1 struct {
	key string
	l   []*cacheEntry2
}

type cacheEntry2 struct {
	sub   string
	value string
}

func newCache() *cache {
	return &cache{
		m: make(map[string]*cacheEntry1),
	}
}

func (c *cache) put(key, sub, value string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	ce1, ok := c.m[key]
	if !ok {
		ce1 = &cacheEntry1{
			key: key,
			l:   make([]*cacheEntry2, 0),
		}
		c.m[key] = ce1
	}

	for _, ce2 := range ce1.l {
		if ce2.sub == sub {
			ce2.value = value
			return
		}
	}

	ce1.l = append(ce1.l, &cacheEntry2{
		sub:   sub,
		value: value,
	})
}

func (c *cache) get(key string) []cacheEntry2 {
	c.lock.RLock()
	defer c.lock.RUnlock()

	l := make([]cacheEntry2, 0)

	ce1, ok := c.m[key]
	if ok {
		for _, ce2 := range ce1.l {
			l = append(l, cacheEntry2{
				sub:   ce2.sub,
				value: ce2.value,
			})
		}
	}

	return l
}

/**
 * HTTP utilities
 */

func setNoCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "max-age=0, no-cache, no-store")
	w.Header().Set("Pragma", "no-cache")
}

func readHttpRequest(r *http.Request, rq interface{}) (int, string) {
	var err error

	defer func() { _ = r.Body.Close() }()

	requestData, err := ioutil.ReadAll(io.LimitReader(r.Body, maxHttpRequestSize))
	if err != nil {
		return http.StatusBadRequest, "Error reading request"
	}

	fmt.Printf("Request %s\n%s\n", r.URL, string(requestData))

	err = json.Unmarshal(requestData, &rq)
	if err != nil {
		return http.StatusBadRequest, fmt.Sprintf("Error parsing request: %s", err)
	}

	return http.StatusOK, ""
}

func sendJsonResponse(w http.ResponseWriter, rs interface{}) {
	w.Header().Set(contentType, respMimeApplicationJson)

	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	err := encoder.Encode(&rs)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
	}
}

/**
 * Cache instance
 */

var gCache = newCache()

/**
 * HTTP put
 */

type rqPut struct {
	Key   string `json:"key"`
	Sub   string `json:"sub"`
	Value string `json:"value"`
}

type rsPut struct {
}

func httpPut(w http.ResponseWriter, r *http.Request) {
	var rq rqPut

	setNoCache(w)

	status, message := readHttpRequest(r, &rq)
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(message))
		return
	}

	gCache.put(rq.Key, rq.Sub, rq.Value)

	rs := rsPut{}
	sendJsonResponse(w, &rs)
}

/**
 * HTTP get
 */

type rqGet struct {
	Key string `json:"key"`
}

type rsGetValue struct {
	Sub   string `json:"sub"`
	Value string `json:"value"`
}

type rsGet struct {
	ValueList []rsGetValue `json:"value_list"`
}

func httpGet(w http.ResponseWriter, r *http.Request) {
	var rq rqGet

	setNoCache(w)

	status, message := readHttpRequest(r, &rq)
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(message))
		return
	}

	valueList := make([]rsGetValue, 0)
	for _, item := range gCache.get(rq.Key) {
		valueList = append(valueList, rsGetValue{
			Sub:   item.sub,
			Value: item.value,
		})
	}

	rs := rsGet{ValueList: valueList}
	sendJsonResponse(w, &rs)
}

/**
 * HTTP loop
 */

func httpLoop(ip net.IP, port int) {
	address := fmt.Sprintf("%s:%d", ip, port)
	err := http.ListenAndServe(address, nil)
	if err != nil {
		fatal("cannot listen on http", err)
	}
}

/**
 * Flags
 */

type Flags struct {
	listenInterface string
	listenAddress   string
	listenPort      int
}

/**
 * Get address for an interface
 */

func findInterfaceAddress(ifaceName string) *net.IP {
	ifaceList, err := net.Interfaces()
	if err != nil {
		fatal("cannot get local interface list", err)
	}
	for _, iface := range ifaceList {
		if iface.Name == ifaceName {
			addrList, err := iface.Addrs()
			if err != nil {
				fatal("cannot get address list", err)
			}

			for _, addr := range addrList {
				switch v := addr.(type) {
				case *net.IPNet:
					fmt.Printf("%v: %s\n", iface.Name, v)
					return &v.IP

					//case *net.IPNet:
					//	fmt.Printf("%v : %s [%v/%v]\n", i.Name, v, v.IP, v.Mask)
				}
			}
		}
	}

	return nil
}

/**
 * Main
 */

func main() {
	fmt.Printf("Hello this is simple discover server\n")

	// Parse flags
	var flags Flags

	flag.StringVar(&flags.listenInterface, "i", "", "Listen interface")
	flag.StringVar(&flags.listenAddress, "a", "", "Listen address")
	flag.IntVar(&flags.listenPort, "p", 65001, "Listen port")
	flag.Parse()

	if flags.listenPort <= 0 || flags.listenPort > 65535 {
		fmt.Printf("Error: invalid listen port %d\n", flags.listenPort)
		os.Exit(1)
	}

	// Listen on HTTP
	http.HandleFunc("/put", httpPut)
	http.HandleFunc("/get", httpGet)

	listenIP := net.IPv4(0, 0, 0, 0)
	if flags.listenInterface != "" {
		// On a specific interface
		findIP := findInterfaceAddress(flags.listenInterface)
		if findIP == nil {
			fatal("cannot find interface address", errors.New(flags.listenAddress))
		}
		listenIP = *findIP
	} else if flags.listenAddress != "" {
		// On a specific address
		listenIP = net.ParseIP(flags.listenAddress)
	}
	listenPort := flags.listenPort

	go httpLoop(listenIP, listenPort)

	// Just wait
	for {
		time.Sleep(time.Minute)
		fmt.Printf("Still running...\n")
	}
}
