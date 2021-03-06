package percy

import (
	"bytes"
	"net"
	"testing"
)

type KdEchoServer struct {
	stopChan chan bool
}

func NewKdEchoServer(port int) (*KdEchoServer, error) {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	stopped := false
	stopChan := make(chan bool)
	packetChan := make(chan packet)

	go func() {
		buf := make([]byte, 2048)

		for !stopped {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}

			buf = buf[:n]
			packetChan <- packet{addr: addr, msg: buf[:n]}
		}
	}()

	go func() {
		var pkt packet

		for {
			select {
			case <-stopChan:
				conn.Close()
				return
			case pkt = <-packetChan:
				pkt.msg = append(pkt.msg, 0x01)
				conn.WriteToUDP(pkt.msg, pkt.addr)
			}
		}
	}()

	return &KdEchoServer{stopChan: stopChan}, nil
}

func (echo *KdEchoServer) Stop() {
	echo.stopChan <- true
}

type assocPacket struct {
	assocID AssociationID
	msg     []byte
}

type MDDChan chan assocPacket

func (mdd MDDChan) Send(assocID AssociationID, msg []byte) error {
	mdd <- assocPacket{assocID, msg}
	return nil
}

func (mdd MDDChan) SetKeys(assocID AssociationID, keys HBHKeys) error {
	return nil
}

func TestUDPForwarder(t *testing.T) {
	port := 2000
	server := "localhost:2000"

	md := make(MDDChan)
	echo, err := NewKdEchoServer(port)
	if err != nil {
		t.Fatalf("Error creating kd echo server: %v", err)
	}

	fwd, err := NewUDPForwarder(server)
	if err != nil {
		t.Fatalf("Error creating echo server: %v", err)
	}

	fwd.MD = md

	var assoc1 AssociationID = 1
	var assoc2 AssociationID = 2
	msgIn := []byte{0x14, 0x00}
	msgOut := []byte{0x14, 0x00, 0x01}

	assocSequence := []AssociationID{assoc1, assoc1, assoc2, assoc2, assoc1, assoc2, assoc1}
	for _, assocID := range assocSequence {
		fwd.Send(assocID, msgIn)
		pkt := <-md

		if pkt.assocID != assocID {
			t.Fatalf("Incorrect association ID: %04x != %04x", pkt.assocID, assocID)
		}
		if !bytes.Equal(pkt.msg, msgOut) {
			t.Fatalf("Incorrect packet message: %x != %x", pkt.msg, msgOut)
		}
	}

	echo.Stop()
}
