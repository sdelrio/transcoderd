package task

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/avast/retry-go"
	"github.com/google/uuid"
	"github.com/isayme/go-amqp-reconnect/rabbitmq"
	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
	"transcoder/broker"
	"transcoder/helper"
	"transcoder/helper/concurrent"
	"transcoder/model"
)

type JobWorker struct {
	jobID        uuid.UUID
	active       bool
	pgs          concurrent.Slice
	pgsWorker    *PGSWorker
	encodeWorker *EncodeWorker
}

func (w JobWorker) GetPGSByID(pgsid int) *TaskPGSJobControl {
	for obj := range w.pgs.Iter() {
		taskPGSJobControl := obj.Value.(*TaskPGSJobControl)
		if taskPGSJobControl.task.PGSID == pgsid {
			return taskPGSJobControl
		}
	}
	return nil
}

func NewBrokerClientRabbit(brokerConfig broker.Config, workerConfig Config, printer *ConsoleWorkerPrinter) *RabbitMQClient {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	uniqueID := fmt.Sprintf("%s-%d", workerConfig.Name, rnd.Intn(5000000))
	queueRabbit := &RabbitMQClient{
		brokerConfig:      brokerConfig,
		workerConfig:      workerConfig,
		workerUniqueQueue: fmt.Sprintf("%s-%s", uniqueID, "control"),
		consumerName:      uniqueID,
		printer:           printer,
		//consumerName: workerConfig.Name,
	}
	return queueRabbit
}

type RabbitMQClient struct {
	consumerName      string
	brokerConfig      broker.Config
	workerConfig      Config
	connection        *rabbitmq.Connection
	workerUniqueQueue string
	PGSWorker         []*JobWorker
	EncodeWorker      *JobWorker
	printer           *ConsoleWorkerPrinter
}

func (Q *RabbitMQClient) RegisterPGSWorker(worker *PGSWorker) {
	worker.Manager = Q
	Q.PGSWorker = append(Q.PGSWorker, &JobWorker{
		active:    false,
		pgsWorker: worker,
		pgs:       concurrent.Slice{},
	})
}

func (Q *RabbitMQClient) RegisterEncodeWorker(worker *EncodeWorker) {
	worker.Manager = Q
	Q.EncodeWorker = &JobWorker{
		active:       false,
		encodeWorker: worker,
		pgs:          concurrent.Slice{},
	}
}

func (Q *RabbitMQClient) Run(wg *sync.WaitGroup, ctx context.Context) {
	log.Info("Starting Broker Client...")
	Q.start(ctx)
	log.Info("Started Broker Client...")
	wg.Add(1)
	go func() {
		<-ctx.Done()
		log.Info("Stopping Broker Client...")
		Q.stop()
		wg.Done()
	}()
}
func (Q *RabbitMQClient) conn() (*rabbitmq.Connection, error) {
	conn, err := rabbitmq.Dial(fmt.Sprintf("amqp://%s:%s@%s:%d/", Q.brokerConfig.User, Q.brokerConfig.Password, Q.brokerConfig.Host, Q.brokerConfig.Port))
	return conn, err

}
func (Q *RabbitMQClient) start(ctx context.Context) {
	conn, err := Q.conn()
	if err != nil {
		log.Panic(err)
	}
	Q.connection = conn

	go Q.eventProcessor(ctx)
}
func (Q *RabbitMQClient) stop() {
	log.Info("waiting for jobs to cancel")
}
func (Q *RabbitMQClient) EventNotification(event model.TaskEvent) {
	//TODO maybe we should set the queueName always?
	err := Q.publishMessage(Q.brokerConfig.TaskEventQueueName, event)
	if err != nil {
		log.Panic(err)
	}

	//log.Infof("[Job %s] %s have been %s", event.Id.String(), event.NotificationType, event.Status)
}
func (Q *RabbitMQClient) RequestPGSJob(pgsJob model.TaskPGS) <-chan *model.TaskPGSResponse {
	pgsJobControl := NewPGSJobControl(pgsJob)
	pgsJob.ReplyTo = Q.workerUniqueQueue
	if err := Q.publishMessage(Q.brokerConfig.TaskPGSToSrtQueueName, pgsJob); err != nil {
		log.Panic(err)
	}

	Q.EncodeWorker.pgs.Append(pgsJobControl)
	return pgsJobControl.response
}
func (Q *RabbitMQClient) ResponsePGSJob(pgsResponse model.TaskPGSResponse) error {
	bytes, err := json.Marshal(pgsResponse)
	if err != nil {
		return err
	}
	message := amqp.Publishing{
		ContentType: "text/plain",
		Body:        bytes,
		Type:        "PGSResponse",
		Priority:    5,
		Timestamp:   time.Now(),
	}
	return Q.publishAMQPMessage(pgsResponse.Queue, message)
}
func (Q *RabbitMQClient) initWorkerQueue(channel *rabbitmq.Channel) error {
	_, err := channel.QueueDeclare(Q.workerUniqueQueue, true, false, true, false, nil)
	return err
}

func (Q *RabbitMQClient) handleWorkerQueue(channel *rabbitmq.Channel) {
	for {
		_, ok := <-channel.Channel.NotifyClose(make(chan *amqp.Error))
		// exit this goroutine if closed by developer
		if !ok || channel.IsClosed() {
			break
		}
	loop:
		for {
			Q.printer.Warn("AMQP Connection lost, recreating worker queue")
			err := Q.initWorkerQueue(channel)
			if err == nil {
				break loop
			}
			time.Sleep(1 * time.Second)
		}
	}

}

func (Q *RabbitMQClient) eventProcessor(ctx context.Context) {
	//Declare Worker Unique Queue
	workerchan, err := Q.connection.Channel()
	if err != nil {
		log.Panic(err)
	}
	err = Q.initWorkerQueue(workerchan)
	if err != nil {
		log.Panic(err)
	}
	go Q.handleWorkerQueue(workerchan)

	workerQueueChan, err := workerchan.Consume(Q.workerUniqueQueue, fmt.Sprintf("%s-%s", Q.consumerName, "control"), false, true, false, false, nil)
	if err != nil {
		log.Panic(err)
	}

	if Q.workerConfig.Jobs.IsAccepted(model.PGSToSrtJobType) {
		go Q.pgsQueueProcessor(ctx, Q.brokerConfig.TaskPGSToSrtQueueName, model.PGSToSrtJobType)
	}
	if Q.workerConfig.Jobs.IsAccepted(model.EncodeJobType) {
		go Q.encodeQueueProcessor(ctx, Q.brokerConfig.TaskEncodeQueueName)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second * 30):
			pingEvent := model.TaskEvent{
				EventType:   model.PingEvent,
				WorkerName:  Q.workerConfig.Name,
				WorkerQueue: Q.workerUniqueQueue,
				EventTime:   time.Now(),
				IP:          helper.GetPublicIP(),
			}
			Q.publishMessageTtl(Q.brokerConfig.TaskEventQueueName, pingEvent, time.Duration(30)*time.Second)
		case rabbitEvent := <-workerQueueChan:
			switch rabbitEvent.Type {
			case "PGSResponse":
				PGSResponse := &model.TaskPGSResponse{}
				Q.ObjectUnmarshall(rabbitEvent, PGSResponse)
				taskPGS := Q.EncodeWorker.GetPGSByID(PGSResponse.PGSID)
				if taskPGS != nil {
					taskPGS.response <- PGSResponse
					close(taskPGS.response)
					Q.EncodeWorker.pgs.Delete(taskPGS)
				}
			}
			rabbitEvent.Ack(false)
		}
	}
}
func (Q *RabbitMQClient) ObjectUnmarshall(rabbitEvent amqp.Delivery, object interface{}) {
	err := json.Unmarshal(rabbitEvent.Body, object)
	if err != nil {
		rabbitEvent.Nack(false, true)
		log.Panic(err)
	}
}
func (Q *RabbitMQClient) pgsQueueProcessor(ctx context.Context, taskQueueName string, jobType model.JobType) {
	channel, err := Q.connection.Channel()
	if err != nil {
		log.Panic(err)
	}
	//Declare Task Queue
	args := amqp.Table{}
	args["x-max-priority"] = 10
	var taskQueue amqp.Queue
	err = retry.Do(func() error {
		taskQueue, err = channel.QueueDeclare(taskQueueName, true, false, false, false, args)
		return err
	}, retry.Delay(time.Second*1), retry.Attempts(10), retry.LastErrorOnly(true), retry.OnRetry(func(n uint, err error) {
		Q.printer.Error("Error on Declare Queue %s:%v", taskQueueName, err)
	}))

	if err != nil {
		log.Panic(err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
			for _, worker := range Q.PGSWorker {
				if !worker.active && worker.pgsWorker.AcceptJobs() {
					delivery, ok, err := channel.Get(taskQueue.Name, false)
					if err != nil || !ok {
						<-time.After(time.Second * 5)
						continue
					}

					if !helper.IsApplicationUpToDate() {
						delivery.Nack(false, true)
						Q.printer.Warn("Application is not up to date, closing...")
						os.Exit(1)
					}

					Q.printer.Log("[%s] Job Assigned to %s", jobType, worker.pgsWorker.GetID())
					if err := worker.pgsWorker.Prepare(delivery.Body, Q); err != nil {
						worker.pgsWorker.Clean()
						delivery.Nack(false, true)
						Q.printer.Error("[%s] Error Preparing Job Execution on %s", jobType, worker.pgsWorker.GetID())
						continue
					}
					worker.jobID = worker.pgsWorker.GetTaskID()
					worker.active = true
					go Q.controlPGSJobExecution(worker)
					delivery.Ack(false)
				}
			}
		}
	}
}

func (Q *RabbitMQClient) encodeQueueProcessor(ctx context.Context, taskQueueName string) {
	channel, err := Q.connection.Channel()
	if err != nil {
		log.Panic(err)
	}
	//Declare Task Queue
	args := amqp.Table{}
	args["x-max-priority"] = 10
	var taskQueue amqp.Queue
	err = retry.Do(func() error {
		taskQueue, err = channel.QueueDeclare(taskQueueName, true, false, false, false, args)
		return err
	}, retry.Delay(time.Second*1), retry.Attempts(10), retry.LastErrorOnly(true), retry.OnRetry(func(n uint, err error) {
		Q.printer.Error("Error on Declare Queue %s:%v", taskQueueName, err)
	}))

	if err != nil {
		log.Panic(err)
	}
	Q.EncodeWorker.encodeWorker.Manager = Q
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
			if Q.EncodeWorker.encodeWorker.AcceptJobs() {
				delivery, ok, err := channel.Get(taskQueue.Name, false)
				if err != nil || !ok {
					<-time.After(time.Second * 5)
					continue
				}

				if !helper.IsApplicationUpToDate() {
					delivery.Nack(false, true)
					Q.printer.Warn("Application is not up to date, waitting for pending jobs to complete, before update...")
					Q.EncodeWorker.encodeWorker.StopQueues()
					<-time.After(time.Second * 2)
					os.Exit(1)
				}

				if err := Q.EncodeWorker.encodeWorker.Execute(delivery.Body); err != nil {
					delivery.Nack(false, true)
					Q.printer.Error("[%s] Error Preparing Job Execution: %v", model.EncodeJobType, err)
					continue
				}
				delivery.Ack(false)
			}
		}
	}
}

func (Q *RabbitMQClient) controlPGSJobExecution(jobWorker *JobWorker) {
	defer func() {
		err := retry.Do(func() error {
			return jobWorker.pgsWorker.Clean()
		}, retry.Delay(time.Second*1), retry.Attempts(3600), retry.LastErrorOnly(true), retry.OnRetry(func(n uint, err error) {
			Q.printer.Error("Error %s for %d time on cleaning working path for worker %s", err.Error(), n, jobWorker.pgsWorker.GetID())
		}))
		if err != nil {
			panic(err)
		}

		jobWorker.active = false
	}()
	jobWorker.pgsWorker.Execute()

}
func (Q *RabbitMQClient) publishMessage(queueName string, obj interface{}) error {
	return Q.publishMessageTtl(queueName, obj, 0)
}
func (Q *RabbitMQClient) publishMessageTtl(queueName string, obj interface{}, ttl time.Duration) error {
	bytes, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	expiration := ""
	if ttl.Milliseconds() > 0 {
		expiration = strconv.FormatInt(ttl.Milliseconds(), 10)
	}
	message := amqp.Publishing{
		Headers:     nil,
		ContentType: "text/plain",
		Priority:    5,
		Expiration:  expiration,
		Timestamp:   time.Now(),
		Body:        bytes,
	}
	return Q.publishAMQPMessage(queueName, message)
}
func (Q *RabbitMQClient) publishAMQPMessage(queueName string, message amqp.Publishing) error {
	return retry.Do(func() error {
		channel, err := Q.connection.Channel()
		if err != nil {
			return err
		}
		defer channel.Close()

		return channel.Publish("", queueName, false, false, message)
	}, retry.Delay(time.Second*1), retry.Attempts(3600), retry.LastErrorOnly(true), retry.OnRetry(func(n uint, err error) {
		Q.printer.Warn("Error %s on publish AMQP Message %s", err.Error(), string(message.Body))
	}))
}

type TaskPGSJobControl struct {
	task     model.TaskPGS
	response chan *model.TaskPGSResponse
}

func NewPGSJobControl(task model.TaskPGS) *TaskPGSJobControl {
	return &TaskPGSJobControl{
		task:     task,
		response: make(chan *model.TaskPGSResponse, 1),
	}
}
