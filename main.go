package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	golog "github.com/ipfs/go-log"
	libp2p "github.com/libp2p/go-libp2p"

	// crypto "github.com/libp2p/go-libp2p-crypto"
	crypto "github.com/libp2p/go-libp2p-core/crypto"
	// host "github.com/libp2p/go-libp2p-host"
	host "github.com/libp2p/go-libp2p-core/host"
	// net "github.com/libp2p/go-libp2p-net"
	// net "github.com/libp2p/go-libp2p-core/network"
	net "github.com/libp2p/go-libp2p-core/network"
	// peer "github.com/libp2p/go-libp2p-peer"
	peer "github.com/libp2p/go-libp2p-core/peer"

	// ma "github.com/multiformats/go-multiaddr"
	ma "github.com/multiformats/go-multiaddr"
)

type Block struct {
	Index     int    // 블록체인 안에서 데이터 레코드의 위치
	Timestamp string // 자동으로 결정되며 데이터가 기록되는 시간
	BPM       int    // 분당 맥박수??
	Hash      string // 데이터 레코드를 나타내는 SHA256 identifier
	PrevHash  string // 체인에 있는 이전 레코드의 SHA256 identifier
}

var Blockchain []Block // 우리의 "state" 또는 최신 블록체인

var mutex = &sync.Mutex{} // 경쟁 조건을 제어하고 방지할 수 있도록 선언

// Block의 데이터를 가져와 SHA256 해시를 생성하는 함수
func calculateHash(block Block) string {
	record := string(block.Index) + block.Timestamp + string(block.BPM) + block.PrevHash
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}

// Block Generate
// 우리가 필요로 하는 블록생성 함수 생성
func generateBlock(oldBlock Block, BPM int) (Block, error) {
	var newBlock Block

	t := time.Now()

	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = t.String()
	newBlock.BPM = BPM
	newBlock.PrevHash = oldBlock.Hash
	newBlock.Hash = calculateHash(newBlock)

	return newBlock, nil
}

// Block Verification
// Index가 예상대로 증가했는지 검사
// PervHash가 이전블록해시와 같은지 검사
// 현재 블록에서 함수를 하시 Hash실행하여 현재 블록의 해시를 다시 확인
// calculateHash를 다시하여 같은 값이 나오는지 검사
// 모든 검사를 통과하면 반환
func isBlockValid(newBlock, oldBlock Block) bool {
	if oldBlock.Index+1 != newBlock.Index {
		return false
	}

	if oldBlock.Hash != newBlock.PrevHash {
		return false
	}

	if calculateHash(newBlock) != newBlock.Hash {
		return false
	}

	return true
}

//makeBasicHost create a LibP2P host with a random PeerID listening on the
//given multiaddress. It will use secio if secio is true.
func makeBasicHost(listenPort int, secio bool, randseed int64) (host.Host, error) {
	// If the seed is zero, use real cryptographic randomness. Otherwise, use a
	// deterministic randomness source to make generated keys stay the same across multiple runs
	var r io.Reader
	if randseed == 0 {
		r = rand.Reader
	} else {
		r = mrand.New(mrand.NewSource(randseed))
	}

	// Generate a key pair for this host. We will use it
	// to obtain a valid host ID.
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	if err != nil {
		return nil, err
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", listenPort)),
		libp2p.Identity(priv),
	}
	if !secio {
		opts = append(opts, libp2p.NoSecurity)
	}

	basicHost, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}

	// Build host multiaddress
	hostAddr, _ := ma.NewMultiaddr(fmt.Sprintf("/ipfs/%s", basicHost.ID().Pretty()))

	// Now we can build a full multiaddress to reach this host
	// by encapsulating both addresses:
	addr := basicHost.Addrs()[0]
	fullAddr := addr.Encapsulate(hostAddr)
	log.Printf("I am %s\n", fullAddr)
	if secio {
		log.Printf("Now run \" go run main.go -l %d -d %s -secio\" on a different terminal\n", listenPort+1, fullAddr)
	} else {
		log.Printf("Now run \" go run main.go -l %d -d %s\" on a different termianl\n", listenPort+1, fullAddr)
	}

	return basicHost, nil
}

// Stream Handler
// Host가 들어오는 데이터 스트림을 처리하도록 허용.
// 다른 노드가 우리 호스트에 연결하고 자신의 블록을 덮어쓸 새로운 블록체인을 proposal할 때 ,
// 우리는 그것을 수락해야 하는지 여부를 결정하는 논리
func handleStream(s net.Stream) {
	log.Println("Got a new stream!!")

	// Create a buffer stream for non blocking read and write.
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	go readData(rw)
	go writeData(rw)

	// stream 's' will stay open until you close it (or the other side close it)
}

func readData(rw *bufio.ReadWriter) {
	for {
		// Peer 에 들어오는 문자열 구문 분석

		str, err := rw.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}
		if str == "" {
			return
		}
		// 문자열이 비어 있지 않은 경우
		if str != "\n" {
			chain := make([]Block, 0)
			if err := json.Unmarshal([]byte(str), &chain); err != nil {
				log.Fatal(err)
			}
			mutex.Lock()
			// Peer에 들어오는 체인의 길이가 우리가 저장하고 있는 블록체인보다 긴지 확인
			// 블록체인의 길이를 기준으로 누가 긴지를 판별
			// 들어오는 체인이 우리가 가진 체인보다 길면 최신 네트워크 상태로 받아 들임
			if len(chain) > len(Blockchain) {
				Blockchain = chain
				bytes, err := json.MarshalIndent(Blockchain, "", "  ") // 읽기 쉽게 JSON 형식으로 만듬
				if err != nil {
					log.Fatal(err)
				}
				// Green console color \x1b[32m
				// Reset console color \x1b[0m
				// fmt.Printf("\x1b[32m%s\x1b[0m> ", string(bytes))
				fmt.Printf("\x1b[32m%s\x1b[0m> ", string(bytes))
			}
			mutex.Unlock()
		}
	}
}

func writeData(rw *bufio.ReadWriter) {
	go func() {
		for {
			// 5초 마다 블록체인을 최신상태로 브로드 캐스트
			time.Sleep(5 * time.Second)
			mutex.Lock()
			bytes, err := json.Marshal(Blockchain)
			if err != nil {
				log.Println(err)
			}
			mutex.Unlock()

			mutex.Lock()
			rw.WriteString(fmt.Sprintf("%s\n", string(bytes)))
			rw.Flush()
			mutex.Unlock()
		}
	}()

	// BPM을 입력하기 위한 콘솔
	stdReader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("> ")
		sendData, err := stdReader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		sendData = strings.Replace(sendData, "\n", "", -1)
		bpm, err := strconv.Atoi(sendData)
		if err != nil {
			log.Fatal(err)
		}

		// 블록 생성
		newBlock, err := generateBlock(Blockchain[len(Blockchain)-1], bpm)
		if err != nil {
			log.Fatal(err)
		}

		if isBlockValid(newBlock, Blockchain[len(Blockchain)-1]) {
			mutex.Lock()
			Blockchain = append(Blockchain, newBlock)
			mutex.Unlock()
		}
		// JSON 형식으로 표현
		bytes, err := json.Marshal(Blockchain)
		if err != nil {
			log.Println(err)
		}
		// JSON 형식을 콘솔에 인쇄
		spew.Dump(Blockchain)

		mutex.Lock()
		// 연결된 Peer에 Broadcasting
		rw.WriteString(fmt.Sprintf("%s\n", string(bytes)))
		rw.Flush()
		mutex.Unlock()
	}
}

func main() {
	t := time.Now()
	genesisBlock := Block{}
	genesisBlock = Block{0, t.String(), 0, calculateHash(genesisBlock), ""}

	Blockchain = append(Blockchain, genesisBlock)

	// libp2p code uses golog to log messages. They log with different
	// string IDs (.i.e. "swarm"). We can control the verbosity
	// all loggers with:
	golog.SetAllLoggers(golog.LevelInfo) //Change to DEBUG for extra info

	// Parse options from the command line

	listenF := flag.Int("l", 0, "wait for incoming connections")
	target := flag.String("d", "", "target peer to dial") // 연결하려는 호스트의 주소
	secio := flag.Bool("secio", false, "enable secio")
	seed := flag.Int64("seed", 0, "set random seed for id generation") // 다른 peer가 우리에게 연결하는데 사용할 수 있는 주소를 구성하는데 사용되는 시더
	flag.Parse()

	if *listenF == 0 {
		log.Fatal("Please provide a port to bind on with -l")
	}

	// Make a host that listens on the given multiaddress
	ha, err := makeBasicHost(*listenF, *secio, *seed)
	if err != nil {
		log.Fatal(err)
	}

	if *target == "" {
		log.Println("listening for connections")
		// Set a stream handler on host A. /p2p/1.0.0 is
		// a user-defined protocol name.
		ha.SetStreamHandler("/p2p/1.0.0", handleStream)

		select {} // hang forever
		/**** This is where the listener code ends ****/
	} else {
		peerMA, err := ma.NewMultiaddr(*target)
		if err != nil {
			log.Fatalln(err)
		}
		peerAddrInfo, err := peer.AddrInfoFromP2pAddr(peerMA)
		if err != nil {
			log.Fatalln(err)
		}

		// Connect to the node at the given address.
		if err := ha.Connect(context.Background(), *peerAddrInfo); err != nil {
			log.Fatalln(err)
		}
		fmt.Println("Connect to ", peerAddrInfo.String())

		// Open a stream with the given peer.
		s, err := ha.NewStream(context.Background(), peerAddrInfo.ID, "/p2p/1.0.0")
		if err != nil {
			log.Fatalln(err)
		}
		// Create a buffered stream so that read and writes are non blocking.
		rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

		// Create a thread to read and write data.
		go writeData(rw)
		go readData(rw)

		select {} // hang forever
	}

}
