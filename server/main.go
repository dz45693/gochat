package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/Shopify/sarama"
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
			//manager.send(jsonMessage)
			syncProducer(jsonMessage)
			//如果连接断开了
		case conn := <-manager.unregister:
			//判断连接的状态，如果是true,就关闭send，删除连接client的值
			if _, ok := manager.clients[conn.id]; ok {
				close(conn.send)
				delete(manager.clients, conn.id)
				jsonMessage, _ := json.Marshal(&Message{Content: "/A socket has disconnected. " + conn.ip, Sender: conn.id})
				//manager.send(jsonMessage)
				syncProducer(jsonMessage)
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
		//manager.broadcast <- jsonMessage
		syncProducer(jsonMessage)
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
	initial()
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
	client := &Client{id: uuid.Must(uuid.NewV4(),nil).String(), socket: conn, send: make(chan []byte), ip: LocalIp()}
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

/////kafka

var topic = "chat"
var producer sarama.SyncProducer

func initial() {
	config := sarama.NewConfig()
	// Version 必须大于等于  V0_10_2_0
	config.Version = sarama.V0_10_2_1
	config.Consumer.Return.Errors = true
	fmt.Println("start connect kafka")
	// 开始连接kafka服务器
	address := []string{"192.168.100.30:9092"}
	client, err := sarama.NewClient(address, config)
	if err != nil {
		fmt.Println("connect kafka failed; err", err)
		return
	}

	groupId := LocalIp()
	group, err := sarama.NewConsumerGroupFromClient(groupId, client)
	if err != nil {
		fmt.Println("connect kafka failed; err", err)
		return
	}
	go ConsumerGroup(group, []string{topic})

	config = sarama.NewConfig()
	// 等待服务器所有副本都保存成功后的响应
	config.Producer.RequiredAcks = sarama.WaitForAll
	// 随机的分区类型：返回一个分区器，该分区器每次选择一个随机分区
	config.Producer.Partitioner = sarama.NewRandomPartitioner
	// 是否等待成功和失败后的响应
	config.Producer.Return.Successes = true
	config.Producer.Timeout = 5 * time.Second
	producer, err = sarama.NewSyncProducer(address, config)
	if err != nil {
		log.Printf("sarama.NewSyncProducer err, message=%s \n", err)
	}
}

//同生产步消息模式
func syncProducer(data []byte) {
	msg := &sarama.ProducerMessage{
		Topic:     topic,
		Partition: 0,
		Value:     sarama.ByteEncoder(data),
	}
	partition, offset, err := producer.SendMessage(msg)
	if err != nil {
		fmt.Println(fmt.Sprintf("Send message Fail %v", err))
	}
	fmt.Printf("send message success topic=%s Partition = %d, offset=%d content:=%s \n", topic, partition, offset ,string(data))
}

func ConsumerGroup(group sarama.ConsumerGroup, topics []string) {
	// 检查错误
	go func() {
		for err := range group.Errors() {
			fmt.Println("group errors : ", err)
		}
	}()
	ctx := context.Background()
	fmt.Println("start get msg")
	// for 是应对 consumer rebalance
	for {
		// 需要监听的主题
		handler := ConsumerGroupHandler{}
		// 启动kafka消费组模式，消费的逻辑在上面的 ConsumeClaim 这个方法里
		err := group.Consume(ctx, topics, handler)

		if err != nil {
			fmt.Println("consume failed; err : ", err)
			return
		}
	}
}

type ConsumerGroupHandler struct{}

func (ConsumerGroupHandler) Setup(sess sarama.ConsumerGroupSession) error {
	//sess.MarkOffset(topic, 0, 0, "")
	return nil
}

func (ConsumerGroupHandler) Cleanup(sess sarama.ConsumerGroupSession) error {
	return nil
}

// 这个方法用来消费消息的
func (h ConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	// 获取消息
	for msg := range claim.Messages() {
		fmt.Printf("kafka receive message %s---Partition:%d, Offset:%d, Key:%s, Value:%s\n", msg.Topic, msg.Partition, msg.Offset, string(msg.Key), string(msg.Value))
		//发送消息
		manager.send(msg.Value)

		// 将消息标记为已使用
		sess.MarkMessage(msg, "")
		sess.Commit()
	}

	return nil
}
