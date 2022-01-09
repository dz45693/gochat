package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	uuid "github.com/satori/go.uuid"
)

//客户端管理
type ClientManager struct {
	//客户端 map 储存并管理所有的长连接client，在线的为true，不在的为false
	clients map[string]*Client
	//web端发送来的的message我们用broadcast来接收，并最后分发给所有的client
	broadcast chan []byte
	//新创建的长连接client
	register chan *Client
	//新注销的长连接client
	unregister chan *Client
}

//客户端 Client
type Client struct {
	//用户id
	id string
	//连接的socket
	socket *websocket.Conn
	//发送的消息
	send chan []byte
	//服务器IP
	ip string
}

//会把Message格式化成json
type Message struct {
	//消息struct
	Sender    string `json:"sender,omitempty"`    //发送者
	Recipient string `json:"recipient,omitempty"` //接收者
	Content   string `json:"content,omitempty"`   //内容
}

//创建客户端管理者
var manager = ClientManager{
	broadcast:  make(chan []byte),
	register:   make(chan *Client),
	unregister: make(chan *Client),
	clients:    make(map[string]*Client),
}

func (manager *ClientManager) start() {
	for {
		select {
		case conn := <-manager.register:
			manager.clients[conn.id] = conn
			//把返回连接成功的消息json格式化
			jsonMessage, _ := json.Marshal(&Message{Content: "/A new socket has connected. " + conn.ip, Sender: conn.id})
			manager.send(jsonMessage)
			//如果连接断开了
		case conn := <-manager.unregister:
			//判断连接的状态，如果是true,就关闭send，删除连接client的值
			if _, ok := manager.clients[conn.id]; ok {
				close(conn.send)
				delete(manager.clients, conn.id)
				jsonMessage, _ := json.Marshal(&Message{Content: "/A socket has disconnected. " + conn.ip, Sender: conn.id})
				manager.send(jsonMessage)
			}
			//广播
		case message := <-manager.broadcast:
			manager.send(message)
		}
	}
}

//定义客户端管理的send方法
func (manager *ClientManager) send(message []byte) {
	obj := &Message{}
	_ = json.Unmarshal(message, obj)
	for id, conn := range manager.clients {
		if obj.Sender == id {
			//continue
		}
		if obj.Recipient == conn.id || len(obj.Recipient) < 1 {
			conn.send <- message
		}
	}
}

//定义客户端结构体的read方法
func (c *Client) read() {
	defer func() {
		manager.unregister <- c
		_ = c.socket.Close()
	}()

	for {
		//读取消息
		_, str, err := c.socket.ReadMessage()
		//如果有错误信息，就注销这个连接然后关闭
		if err != nil {
			manager.unregister <- c
			_ = c.socket.Close()
			break
		}
		//如果没有错误信息就把信息放入broadcast
		message := &Message{}
		_ = json.Unmarshal(str, message)
		message.Sender = c.id
		jsonMessage, _ := json.Marshal(&message)
		fmt.Println(fmt.Sprintf("read Id:%s, msg:%s", c.id, string(jsonMessage)))
		manager.broadcast <- jsonMessage
	}
}

func (c *Client) write() {
	defer func() {
		_ = c.socket.Close()
	}()

	for {
		select {
		//从send里读消息
		case message, ok := <-c.send:
			//如果没有消息
			if !ok {
				_ = c.socket.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			//有消息就写入，发送给web端
			_ = c.socket.WriteMessage(websocket.TextMessage, message)
			fmt.Println(fmt.Sprintf("write Id:%s, msg:%s", c.id, string(message)))
		}
	}
}

func main() {
	fmt.Println("Starting application...")
	//开一个goroutine执行开始程序
	go manager.start()
	//注册默认路由为 /ws ，并使用wsHandler这个方法
	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/health", healthHandler)
	//监听本地的8011端口
	fmt.Println("chat server start.....")
	_ = http.ListenAndServe(":8080", nil)
}

func wsHandler(res http.ResponseWriter, req *http.Request) {
	//将http协议升级成websocket协议
	conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}).Upgrade(res, req, nil)
	if err != nil {
		http.NotFound(res, req)
		return
	}

	//每一次连接都会新开一个client，client.id通过uuid生成保证每次都是不同的
	client := &Client{id: uuid.Must(uuid.NewV4(), nil).String(), socket: conn, send: make(chan []byte), ip: LocalIp()}
	//注册一个新的链接
	manager.register <- client

	//启动协程收web端传过来的消息
	go client.read()
	//启动协程把消息返回给web端
	go client.write()
}

func healthHandler(res http.ResponseWriter, _ *http.Request) {
	_, _ = res.Write([]byte("ok"))
}

func LocalIp() string {
	address, _ := net.InterfaceAddrs()
	var ip = "localhost"
	for _, address := range address {
		if ipAddress, ok := address.(*net.IPNet); ok && !ipAddress.IP.IsLoopback() {
			if ipAddress.IP.To4() != nil {
				ip = ipAddress.IP.String()
			}
		}
	}
	return ip
}
