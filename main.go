package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"net"
	"sort"
	"strings"
	"syscall"
	"time"
	"sync"
)

/* TODO
	follow http redirects
	starttls
	add www, mx, mail....
*/

type DomainNode struct {
	Domain string
	Depth  int
    Neighbors *[]string
}

// vars
var conf = &tls.Config{
	InsecureSkipVerify: true,
}
var markedDomains = make(map[string]bool)
var domainGraph = make(map[string]*DomainNode)
var timeout time.Duration
var port string
var verbose bool
var depth int
var maxDepth int
var parallel int

func v(a ...interface{}) {
	if verbose {
		fmt.Fprintln(os.Stderr, a...)
	}
}


func checkNetErr(err error, domain string) bool {
	if err == nil {
		return false

	} else if netError, ok := err.(net.Error); ok && netError.Timeout() {
		v("Timeout", domain)
	} else {
		switch t := err.(type) {
		case *net.OpError:
			if t.Op == "dial" {
				v("Unknown host", domain)
			} else if t.Op == "read" {
				v("Connection refused", domain)
			}

		case syscall.Errno:
			if t == syscall.ECONNREFUSED {
				v("Connection refused", domain)
			}
		}
	}
	return true
}

/*
* given a domain returns the non-wildecard version of that domain
 */
func directDomain(domain string) string {
	if len(domain) < 3 {
		return domain
	}
	if domain[0:2] == "*." {
		domain = domain[2:]
	}
	return domain
}

func printGraph() {
	// print map in sorted order
	domains := make([]string, 0, len(domainGraph))
	for domain, _ := range domainGraph {
		domains = append(domains, domain)
	}
	sort.Strings(domains)

	for _, domain := range domains {
		fmt.Println(domain, domainGraph[domain].Depth, *domainGraph[domain].Neighbors)
	}
}


func main() {
	host := flag.String("host", "localhost", "Host to Scan")
	flag.StringVar(&port, "port", "443", "Port to connect to")
	timeoutPtr := flag.Int("timeout", 5, "TCP Timeout in seconds")
	flag.BoolVar(&verbose, "verbose", false, "Verbose logging")
	flag.IntVar(&maxDepth, "depth", 20, "Maximum BFS depth to go")
	flag.IntVar(&parallel, "parallel", 10, "Number of certificates to retrieve in parallel")

	flag.Parse()
	if parallel < 1 {
		fmt.Fprintln(os.Stderr, "Must enter a positive number of parallel threads")
		return
	}
	timeout = time.Duration(*timeoutPtr) * time.Second
	startDomain := strings.ToLower(*host)

	BFS(startDomain)

	v("Done...")

	printGraph()

	v("Found", len(domainGraph), "domains") // todo verify
	v("Graph Depth:", depth)

}

func BFS(root string) {
	// parallel code
	var wg sync.WaitGroup
	domainChan := make(chan *DomainNode, 5)
	domainGraphChan := make(chan *DomainNode, 5)

	// thread limit code
	threadPass := make(chan bool, parallel)
	for i:=0; i< parallel; i++ {
		threadPass <-true
	}

	wg.Add(1)
	domainChan <- &DomainNode{root, 0, nil}
	go func() {
		for {
			domainNode := <- domainChan

			// depth check
			if domainNode.Depth > maxDepth {
				v("Max depth reached, skipping:", domainNode.Domain)
				wg.Done()
				continue
			}
			if domainNode.Depth > depth {
				depth = domainNode.Depth
			}

			dDomain := directDomain(domainNode.Domain)
			if !markedDomains[dDomain] {
				markedDomains[dDomain] = true
				go func(domainNode *DomainNode) {
				 	defer wg.Done()
				 	// wait for pass
				 	<-threadPass
				 	defer func() {threadPass <- true}()

					// do things
					dDomain := directDomain(domainNode.Domain)
					v("Visiting", domainNode.Depth, dDomain)
					neighbors := BFSPeers(dDomain) // visit
					domainNode.Neighbors = &neighbors
					domainGraphChan <- domainNode
					for _, neighbor := range neighbors {
						wg.Add(1)
						domainChan <- &DomainNode{neighbor, domainNode.Depth + 1, nil}
					}
				}(domainNode)
			} else {
				wg.Done()
			}
		}
	}()

	// save thread
	done := make(chan bool)
	go func() {
		for {
			domainNode, more := <- domainGraphChan
			if more {
				dDomain := directDomain(domainNode.Domain)
				domainGraph[dDomain] = domainNode // not thread safe
			} else {
				done <- true
				return
			}
		}
	}()

	wg.Wait() // wait for querying to finish
	close(domainGraphChan)
	<-done // wait for save to finish
}

func BFSPeers(host string) []string {
	domains := make([]string, 0)
	certs := getPeerCerts(host)

	if len(certs) == 0 {
		return domains
	}

	// used to ensure uniq entries in domains array
	domainMap := make(map[string]bool)

	// add the CommonName just to be safe
	if len(certs) > 0 {
		cn := strings.ToLower(certs[0].Subject.CommonName)
		if len(cn) > 0 {
			domainMap[cn] = true
		}
	}

	for _, cert := range certs {
		for _, domain := range cert.DNSNames {
			if len(domain) > 0 {
				domain = strings.ToLower(domain)
				domainMap[domain] = true
			}
		}
	}

	for domain, _ := range domainMap {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	return domains

}

func getPeerCerts(host string) []*x509.Certificate {
	addr := net.JoinHostPort(host, port)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, conf)
	if checkNetErr(err, host) {
		return make([]*x509.Certificate, 0)

	}
	conn.Close()
	connState := conn.ConnectionState()
	return connState.PeerCertificates
}
