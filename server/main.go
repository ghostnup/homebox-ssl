package main

import (
	"io"
	"strconv"
	"time"
	"fmt"
	"os"
	"log"
	"net"
	"flag"
	"bytes"
	"net/http"
	"crypto/tls"

	"github.com/gin-gonic/gin"
)

var (
	ENV string
)

const (
	READ_PACK_SIZE  = 1 * 1024 * 1024
	WRITE_PACK_SIZE = 1 * 1024 * 1024
)

var (
	port       int
	caroots    string
	keyfile    string
	signcert   string
)

func init() {
	flag.IntVar(&port,          "port",     3300,       "The host port on which the REST server will listen")
	flag.StringVar(&keyfile,    "key",      "",         "Path to file containing PEM-encoded key file for service")
	flag.StringVar(&signcert,   "signcert", "",         "Path to file containing PEM-encoded sign certificate for service")
}

// Start a proxy server listen on listenport
// This proxy will forward all HTTP request to httpport, and all HTTPS request to httpsport
func proxyStart(listenport, httpport, httpsport int) {
	proxylistener, err := net.Listen("tcp", fmt.Sprintf(":%d", listenport))
	if err != nil {
		fmt.Println("Unable to listen on: %d, error: %s\n", listenport, err.Error())
		os.Exit(1)
	}
	defer proxylistener.Close()

	for {
		proxyconn, err := proxylistener.Accept()
		if err != nil {
			fmt.Printf("Unable to accept a request, error: %s\n", err.Error())
			continue
		}

		// Read a header firstly in case you could have opportunity to check request
		// whether to decline or proceed the request
		buffer := make([]byte, 1024)
		n, err := proxyconn.Read(buffer)
		if err != nil {
			//fmt.Printf("Unable to read from input, error: %s\n", err.Error())
			continue
		}

		var targetport int
		if isHTTPRequest(buffer) {
			targetport = httpport
		} else {
			targetport = httpsport
		}

		targetconn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", targetport))
		if err != nil {
			fmt.Printf("Unable to connect to: %d, error: %s\n", targetport, err.Error())
			proxyconn.Close()
			continue
		}

		n, err = targetconn.Write(buffer[:n])
		if err != nil {
			fmt.Printf("Unable to write to output, error: %s\n", err.Error())
			proxyconn.Close()
			targetconn.Close()
			continue
		}

		go proxyRequest(proxyconn, targetconn)
		go proxyRequest(targetconn, proxyconn)
	}
}

// Forward all requests from r to w
func proxyRequest(r net.Conn, w net.Conn) {
	defer r.Close()
	defer w.Close()

	var buffer = make([]byte, 4096000)
	for {
		n, err := r.Read(buffer)
		if err != nil {
			//fmt.Printf("Unable to read from input, error: %s\n", err.Error())
			break
		}

		n, err = w.Write(buffer[:n])
		if err != nil {
			fmt.Printf("Unable to write to output, error: %s\n", err.Error())
			break
		}
	}
}

func isHTTPRequest(buffer []byte) bool {
	httpMethod := []string{"GET", "PUT", "HEAD", "POST", "DELETE", "PATCH", "OPTIONS"}
	for cnt := 0; cnt < len(httpMethod); cnt++ {
		if bytes.HasPrefix(buffer, []byte(httpMethod[cnt])) {
			return true
		}
	}
	return false
}

func startHTTPSServer(address string, router *gin.Engine, keyfile string, signcert string) {
    _, err1 := os.Stat(keyfile)
    keyLacks := err1 != nil
    _, err2 := os.Stat(signcert)
    certLacks := err2 != nil
	if keyLacks || certLacks {
		log.Println("ListenAndServeTLS closed: no provide key or cert.")
		return
	}

	s := &http.Server{
		Addr:    address,
		Handler: router,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	err := s.ListenAndServeTLS(signcert, keyfile)
	if err != nil {
		log.Fatalln("ListenAndServeTLS err:", err)
	}
}

func startHTTPServer(address string, router *gin.Engine) {
	err := http.ListenAndServe(address, router)
	if err != nil {
		log.Fatalln("ListenAndServe err:", err)
	}
}

func main() {
	isProd := ENV == "production"
	if isProd {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	if isProd {
		//r.LoadHTMLGlob("./static/*.html")
		//r.Static("/static", "./static")
		r.StaticFS("/static", AssetFile())
		html, _ := Asset("index.html")

		r.GET("/", func(ctx *gin.Context) {
			ctx.Header("Content-Type", "text/html")
			ctx.Stream(func(w io.Writer) bool {
				w.Write(html)
				return false
			})
		})
	}

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
	})

	r.OPTIONS("/upload", func(c *gin.Context) {
	//	c.Header("Access-Control-Allow-Origin", "*")
	})

	r.POST("/upload", func(c *gin.Context) {
		//c.Header("Access-Control-Allow-Origin", "*")
		body := c.Request.Body
		data := make([]byte, READ_PACK_SIZE)
		length := 0
		now := time.Now()
		for {
			n, err := body.Read(data)
			length += n
			if err != nil {
				if err != io.EOF {
					c.JSON(400, gin.H{
						"error": err.Error(),
					})
					return
				}
				break
			}
		}
		duration := time.Now().UnixNano() - now.UnixNano()

		c.JSON(200, gin.H{
			"length":   length,
			"duration": float64(duration) / 1000 / 1000,
			"rate":     float64(length) / float64(duration) * 1000 * 1000 * 1000,
		})
	})

	r.GET("/download", func(c *gin.Context) {
		//c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Header("Content-Disposition", "attachment; filename=random.dat")
		c.Header("Content-Transfer-Encoding", "binary")
		packCount, err := strconv.ParseInt(c.Query("count"), 10, 64)
		if err != nil {
			packCount = 8
		}
		packSize, err := strconv.ParseInt(c.Query("size"), 10, 64)

		if err != nil {
			packSize = WRITE_PACK_SIZE
		}

		data := make([]byte, WRITE_PACK_SIZE)

		c.Header("Content-Length", strconv.FormatInt(packCount*packSize, 10))

		i := packCount
		c.Stream(func(w io.Writer) bool {
			w.Write(data)
			i -= 1
			return i > 0
		})
	})

	r.GET("/ping", func(c *gin.Context) {
		//c.Header("Access-Control-Allow-Origin", "*")
		c.Writer.Flush()
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	//fmt.Println("Listened on :3300")

	//r.Run(":3300")

	flag.Parse()
	fmt.Println("Listened on :", port)

	listenport, httpport, httpsport := port, port + 10, port + 20
	go startHTTPServer (fmt.Sprintf("localhost:%d", httpport), r)
	go startHTTPSServer(fmt.Sprintf("localhost:%d", httpsport), r, keyfile, signcert)

	proxyStart(listenport, httpport, httpsport)

	fmt.Println("Exit homebox")
}
