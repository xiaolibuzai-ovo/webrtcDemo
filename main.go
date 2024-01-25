package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
)

func checkError(err error) {
	if err != nil {
		log.Panic(err)
	}
}

func main() {
	router := gin.Default()
	router.LoadHTMLGlob("templates/*.html")
	router.GET("/", func(c *gin.Context) {
		// 渲染并返回 HTML 模板
		c.HTML(200, "demo.html", gin.H{})
	})
	router.GET("/ws", func(c *gin.Context) {
		wsConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		checkError(err)

		// Set the handler to handle input SDP (Offer type)
		handleOffer(wsConn)
	})
	router.Run(":8080")
}

func handleOffer(wsConn *websocket.Conn) {
	// 创建一个MediaEngine对象来配置支持的编解码器
	m := &webrtc.MediaEngine{}

	// 设置要使用的编解码器。
	// 我们将使用VP8和Opus，但您也可以定义自己的
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	//创建一个InterceptorRegistry。这是用户可配置的RTP/RTCP管道。
	//提供了nack、RTCP报告和其他功能。如果你使用`webrtc.js `。NewPeerConnection”
	//默认启用如果您正在手动管理，则必须创建一个InterceptorRegistry 对于每个PeerConnection。
	i := &interceptor.Registry{}

	//注册一个interval工厂
	//这个拦截器每3秒发送一个PLIPLI导致视频关键帧由发送端生成。
	//这使得我们的视频更容易搜索，更具有容错能力，但代价是图像质量降低和比特率提高
	//现实中的应用程序应该处理来自查看者的RTCP数据包，并将它们转发给发送者
	intervalPliFactory, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		panic(err)
	}
	i.Add(intervalPliFactory)

	//使用默认的拦截器集
	if err = webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(err)
	}

	//使用MediaEngine创建API对象
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))
	// configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// 允许我们接收一个音轨和一个视频轨
	if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	} else if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}

	oggFile, err := oggwriter.New("output.ogg", 48000, 2)
	if err != nil {
		panic(err)
	}
	ivfFile, err := ivfwriter.New("output.ivf")
	if err != nil {
		panic(err)
	}
	//设置一个处理程序，当新的远程音轨启动时，这个处理程序将缓冲区保存到磁盘as
	//一个ivf文件，因为我们可以有多个视频轨迹，所以我们提供了一个计数器。
	//在你的应用程序中，这是处理视频的地方
	// 处理远端媒体连接
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		codec := track.Codec()
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeOpus) {
			fmt.Println("Got Opus track, saving to disk as output.opus (48 kHz, 2 channels)")
			saveToDisk(oggFile, track)
		} else if strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8) {
			fmt.Println("Got VP8 track, saving to disk as output.ivf")
			saveToDisk(ivfFile, track)
		}
	})

	//设置ICE连接状态的处理程序
	//当另一端连接或断开时，它会通知你
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())

		if connectionState == webrtc.ICEConnectionStateConnected {
			fmt.Println("Ctrl+C the remote client to stop the demo")
		} else if connectionState == webrtc.ICEConnectionStateFailed || connectionState == webrtc.ICEConnectionStateClosed {
			if closeErr := oggFile.Close(); closeErr != nil {
				panic(closeErr)
			}

			if closeErr := ivfFile.Close(); closeErr != nil {
				panic(closeErr)
			}

			fmt.Println("Done writing media files")

			//正常关闭对端连接
			if closeErr := peerConnection.Close(); closeErr != nil {
				panic(closeErr)
			}

			os.Exit(0)
		}
	})

	//设置对等连接状态的处理程序
	//当另一端连接或断开时，它会通知你
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if s == webrtc.PeerConnectionStateClosed {
			// PeerConnection was explicitly closed. This usually happens from a DTLS CloseNotify
			fmt.Println("Peer Connection has gone to closed exiting")
			os.Exit(0)
		}
	})

	_, msg, err := wsConn.ReadMessage()
	checkError(err)
	offer := webrtc.SessionDescription{}
	err = json.Unmarshal(msg, &offer)
	checkError(err)
	fmt.Println("success offer")
	// Set the remote SessionDescription
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		fmt.Println(err)
		return
	}

	//阻塞直到ICE Gathering完成，禁用涓流ICE
	//我们这样做是因为我们只能交换一个信令消息
	//在生产应用程序中，你应该通过oniceccandidate交换ICE候选者
	<-gatherComplete

	// 发送 answer 给客户端
	marshal, _ := json.Marshal(answer)
	err = wsConn.WriteMessage(websocket.TextMessage, marshal)
	checkError(err)
}

func saveToDisk(i media.Writer, track *webrtc.TrackRemote) {
	defer func() {
		if err := i.Close(); err != nil {
			panic(err)
		}
	}()

	for {
		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			fmt.Println(err)
			return
		}
		if err := i.WriteRTP(rtpPacket); err != nil {
			fmt.Println(err)
			return
		}
	}
}

const (
	audioFileName   = "output.ogg"
	videoFileName   = "output.ivf"
	oggPageDuration = time.Millisecond * 20
)
