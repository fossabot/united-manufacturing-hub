package kafka_processor

import (
	"github.com/Shopify/sarama"
	"github.com/united-manufacturing-hub/Sarama-Kafka-Wrapper/pkg/kafka"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/mqtt-kafka-bridge/message"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/internal"
	"go.uber.org/zap"
	"os"
	"regexp"
	"strings"
	"time"
)

var client *kafka.Client

func Init(kafkaToMqttChan chan kafka.Message, sChan chan bool) {
	if client != nil {
		return
	}
	KafkaBoostrapServer, KafkaBoostrapServerEnvSet := os.LookupEnv("KAFKA_BOOTSTRAP_SERVER")
	if !KafkaBoostrapServerEnvSet {
		zap.S().Fatal("Kafka Bootstrap server (KAFKA_BOOTSTRAP_SERVER) must be set")
	}
	KafkaTopic, KafkaTopicEnvSet := os.LookupEnv("KAFKA_LISTEN_TOPIC")
	if !KafkaTopicEnvSet {
		zap.S().Fatal("Kafka topic (KAFKA_LISTEN_TOPIC) must be set")
	}

	compile, err := regexp.Compile(KafkaTopic)
	if err != nil {
		zap.S().Fatalf("Error compiling regex: %v", err)
	}

	client, err = kafka.NewKafkaClient(kafka.NewClientOptions{
		Brokers: []string{
			KafkaBoostrapServer,
		},
		ConsumerName:      "mqtt-kafka-bridge",
		ListenTopicRegex:  compile,
		Partitions:        6,
		ReplicationFactor: 1,
		EnableTLS:         false,
		StartOffset:       sarama.OffsetOldest,
	})
	if err != nil {
		zap.S().Fatalf("Error creating kafka client: %v", err)
		return
	}
	go processIncomingMessage(kafkaToMqttChan)
}

func processIncomingMessage(kafkaToMqttChan chan kafka.Message) {
	for {
		msg := <-client.GetMessages()
		if message.IsValidKafkaMessage(msg) {
			kafkaToMqttChan <- kafka.Message{
				Topic:  msg.Topic,
				Value:  msg.Value,
				Header: msg.Header,
				Key:    msg.Key,
			}
		}
	}
}

func Shutdown() {
	zap.S().Info("Shutting down kafka client")
	err := client.Close()
	if err != nil {
		zap.S().Fatalf("Error closing kafka client: %v", err)
	}
	zap.S().Info("Kafka client shut down")
}

func Start(mqttToKafkaChan chan kafka.Message) {
	go start(mqttToKafkaChan)
}

func start(mqttToKafkaChan chan kafka.Message) {
	for {
		msg := <-mqttToKafkaChan
		internal.AddSXOrigin(&msg)
		var err error
		err = internal.AddSXTrace(&msg)
		if err != nil {
			zap.S().Fatalf("Failed to marshal trace")
			continue
		}
		// Change MQTT to Kafka topic format
		msg.Topic = strings.ReplaceAll(msg.Topic, "/", ".")

		err = client.EnqueueMessage(msg)
		for err != nil {
			time.Sleep(10 * time.Millisecond)
			err = client.EnqueueMessage(msg)
		}
	}
}

func GetStats() (sent uint64, received uint64) {
	return kafka.GetKafkaStats()
}
