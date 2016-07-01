package percy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"time"
)

type dtlsSRTPPacketClass uint8

const (
	packetClassDTLS dtlsSRTPPacketClass = iota
	packetClassSRTP
	packetClassSTUN
	packetClassUnknown
)

// https://tools.ietf.org/html/rfc5764#section-5.1.2
func packetClass(msg []byte) dtlsSRTPPacketClass {
	if len(msg) == 0 {
		return packetClassUnknown
	}

	// XXX: We could do more validation that DTLS and SRTP
	//      packets are well-formed
	B := msg[0]
	switch {
	case 127 < B && B < 192:
		return packetClassSRTP
	case 19 < B && B < 64:
		return packetClassDTLS
	case B < 2:
		return packetClassSTUN
	default:
		return packetClassUnknown
	}
}

type packet struct {
	addr *net.UDPAddr
	msg  []byte
}

func addrToAssoc(addr *net.UDPAddr) AssociationID {
	var assoc AssociationID
	h := sha256.New()
	h.Write([]byte(addr.String()))
	sum := h.Sum(nil)
	copy(assoc[:], sum[:16])
	return assoc
}

func assocToString(assoc AssociationID) string {
	return hex.EncodeToString(assoc[:])
}

func stringToAssoc(clientID string) (AssociationID, error) {
	var assoc AssociationID
	decoded, err := hex.DecodeString(clientID)
	if err != nil {
		return AssociationID{}, err
	}
	copy(assoc[:], decoded)
	return assoc, nil
}

type MDD struct {
	name       string
	addr       *net.UDPAddr
	conn       *net.UDPConn
	clients    map[string]*net.UDPAddr
	stopChan   chan bool
	doneChan   chan bool
	packetChan chan packet
	timeout    time.Duration

	kmf      KMFTunnel
	keys     *SRTPKeys
	profile  ProtectionProfile
	profiles []ProtectionProfile
	// TODO add some mutexes
}

func NewMDD(kmf KMFTunnel) *MDD {
	mdd := new(MDD)
	mdd.name = "mdd"
	mdd.clients = map[string]*net.UDPAddr{}
	mdd.kmf = kmf
	mdd.timeout = 10 * time.Millisecond

	mdd.stopChan = make(chan bool)
	mdd.doneChan = make(chan bool)
	mdd.packetChan = make(chan packet)

	// TODO Add some defaults
	mdd.profiles = []ProtectionProfile{}

	return mdd
}

func (mdd *MDD) handleDTLS(assocID AssociationID, msg []byte) {
	// Rough check for ClientHello
	ch := len(msg) >= 14 && msg[0] == 0x16 && msg[13] == 0x01

	if ch {
		mdd.kmf.SendWithProfiles(assocID, msg, mdd.profiles)
	} else {
		mdd.kmf.Send(assocID, msg)
	}
}

func (mdd *MDD) handleSRTP(clientID string, msg []byte) {
	if mdd.keys != nil && mdd.profile != 0 {
		// XXX: Here's where you mess with the packet if you want to
	}

	// Send the packet out to all the clients except
	// the one that sent it
	for client, addr := range mdd.clients {
		if client == clientID {
			continue
		}

		_, err := mdd.conn.WriteToUDP(msg, addr)
		if err != nil {
			log.Println("Error forwarding packet")
		}
	}
}

func (mdd *MDD) Listen(port int) error {
	var err error

	mdd.addr = &net.UDPAddr{Port: port}
	mdd.conn, err = net.ListenUDP("udp", mdd.addr)
	if err != nil {
		return err
	}

	mdd.packetChan = make(chan packet, 10)

	go func(packetChan chan packet) {
		buf := make([]byte, 2048)

		for {
			n, addr, err := mdd.conn.ReadFromUDP(buf)

			if err == nil {
				packetChan <- packet{addr: addr, msg: buf[:n]}
			}
			// TODO log errors
		}
	}(mdd.packetChan)

	go func(mdd *MDD) {
		for {
			var pkt packet

			select {
			case <-mdd.stopChan:
				mdd.doneChan <- true
				return
			case <-time.After(mdd.timeout):
				continue
			case pkt = <-mdd.packetChan:
			}

			if err != nil {
				log.Printf("Recv Error: %v", err)
				continue
			}

			assocID := addrToAssoc(pkt.addr)
			clientID := assocToString(assocID)

			// Remember the client if it's new
			// XXX: Could have an interface to add/remove clients, then
			//      just filter unknown clients here.
			if _, ok := mdd.clients[clientID]; !ok {
				mdd.clients[clientID] = pkt.addr
			}

			switch packetClass(pkt.msg) {
			case packetClassDTLS:
				mdd.handleDTLS(assocID, pkt.msg)
			case packetClassSRTP:
				mdd.handleSRTP(clientID, pkt.msg)
			case packetClassSTUN:
				fallthrough
			default:
				log.Printf("Unknown packet type received")
			}
		}
	}(mdd)

	return nil
}

func (mdd *MDD) Send(assoc AssociationID, msg []byte) error {
	clientID := assocToString(assoc)

	if packetClass(msg) != packetClassDTLS {
		return fmt.Errorf("Send called with non-DTLS packet")
	}

	addr, ok := mdd.clients[clientID]
	if !ok {
		return fmt.Errorf("Unknown client [%s]", clientID)
	}

	_, err := mdd.conn.WriteToUDP(msg, addr)
	return err
}

func (mdd *MDD) SendWithKeys(assoc AssociationID, msg []byte, profile ProtectionProfile, keys SRTPKeys) error {
	if packetClass(msg) != packetClassDTLS {
		return fmt.Errorf("Send called with non-DTLS packet")
	}

	mdd.profile = profile
	mdd.keys = &keys
	return mdd.Send(assoc, msg)
}

func (mdd *MDD) Stop() {
	mdd.stopChan <- true
	<-mdd.doneChan

	mdd.conn.Close()

	// Avoid race conditions
	<-time.After(10 * time.Millisecond)
}
