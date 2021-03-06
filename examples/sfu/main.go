package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/pions/rtcp"
	"github.com/pions/rtp"
	"github.com/pions/webrtc"
	"github.com/pions/webrtc/examples/util"
)

var peerConnectionConfig = webrtc.RTCConfiguration{
	IceServers: []webrtc.RTCIceServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},
}

func mustReadStdin(reader *bufio.Reader) string {
	rawSd, err := reader.ReadString('\n')
	util.Check(err)
	fmt.Println("")

	return rawSd
}

func mustReadHTTP(sdp chan string) string {
	ret := <-sdp
	return ret
}

const (
	rtcpPLIInterval = time.Second * 3
)

func main() {
	port := flag.Int("port", 8080, "http server port")
	flag.Parse()

	sdp := make(chan string)
	http.HandleFunc("/sdp", func(w http.ResponseWriter, r *http.Request) {
		body, _ := ioutil.ReadAll(r.Body)
		fmt.Fprintf(w, "done")
		sdp <- string(body)
	})

	go func() {
		err := http.ListenAndServe(":"+strconv.Itoa(*port), nil)
		util.Check(err)
	}()

	offer := webrtc.RTCSessionDescription{}
	util.Decode(mustReadHTTP(sdp), &offer)
	fmt.Println("")

	/* Everything below is the pion-WebRTC API, thanks for using it! */

	// Only support VP8, this makes our proxying code simpler
	webrtc.RegisterCodec(webrtc.NewRTCRtpVP8Codec(webrtc.DefaultPayloadTypeVP8, 90000))

	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewRTCPeerConnection(peerConnectionConfig)
	util.Check(err)

	inboundSSRC := make(chan uint32)
	inboundPayloadType := make(chan uint8)

	outboundRTP := []chan<- *rtp.Packet{}
	var outboundRTPLock sync.RWMutex
	// Set a handler for when a new remote track starts, this just distributes all our packets
	// to connected peers
	peerConnection.OnTrack(func(track *webrtc.RTCTrack) {
		// Send a PLI on an interval so that the publisher is pushing a keyframe every rtcpPLIInterval
		// This can be less wasteful by processing incoming RTCP events, then we would emit a NACK/PLI when a viewer requests it
		go func() {
			ticker := time.NewTicker(rtcpPLIInterval)
			for range ticker.C {
				if err := peerConnection.SendRTCP(&rtcp.PictureLossIndication{MediaSSRC: track.Ssrc}); err != nil {
					fmt.Println(err)
				}
			}
		}()

		inboundSSRC <- track.Ssrc
		inboundPayloadType <- track.PayloadType

		for {
			rtpPacket := <-track.Packets

			outboundRTPLock.RLock()
			for _, outChan := range outboundRTP {
				outPacket := rtpPacket
				outPacket.Payload = append([]byte{}, outPacket.Payload...)
				select {
				case outChan <- outPacket:
				default:
				}
			}
			outboundRTPLock.RUnlock()
		}
	})

	// Set the remote SessionDescription
	util.Check(peerConnection.SetRemoteDescription(offer))

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	util.Check(err)

	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(answer)
	util.Check(err)

	// Get the LocalDescription and take it to base64 so we can paste in browser
	fmt.Println(util.Encode(answer))

	outboundSsrc := <-inboundSSRC
	outboundPayloadType := <-inboundPayloadType
	for {
		fmt.Println("")
		fmt.Println("Curl an base64 SDP to start sendonly peer connection")

		recvOnlyOffer := webrtc.RTCSessionDescription{}
		util.Decode(mustReadHTTP(sdp), &recvOnlyOffer)

		// Create a new RTCPeerConnection
		peerConnection, err := webrtc.NewRTCPeerConnection(peerConnectionConfig)
		util.Check(err)

		// Create a single VP8 Track to send videa
		vp8Track, err := peerConnection.NewRawRTPTrack(outboundPayloadType, outboundSsrc, "video", "pion")
		util.Check(err)

		_, err = peerConnection.AddTrack(vp8Track)
		util.Check(err)

		outboundRTPLock.Lock()
		outboundRTP = append(outboundRTP, vp8Track.RawRTP)
		outboundRTPLock.Unlock()

		// Set the remote SessionDescription
		err = peerConnection.SetRemoteDescription(recvOnlyOffer)
		util.Check(err)

		// Create answer
		answer, err := peerConnection.CreateAnswer(nil)
		util.Check(err)

		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnection.SetLocalDescription(answer)
		util.Check(err)

		// Get the LocalDescription and take it to base64 so we can paste in browser
		fmt.Println(util.Encode(answer))
	}
}
