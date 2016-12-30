package main

import (
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	api "github.com/hashicorp/consul/api"
	observer "github.com/imkira/go-observer"
	uuid "github.com/satori/go.uuid"
	"gopkg.in/fatih/set.v0"
)

// ConsulConnection monitors the websocket connection. It processes any action
// received on the websocket and sends out actions on Consul state changes. It
// maintains a set to keep track of the running watches.
type ConsulConnection struct {
	ID                uuid.UUID
	shortID           string
	socket            *websocket.Conn
	receive           chan *Action
	send              chan *Action
	destroyCh         chan struct{}
	watches           *set.Set
	hub               *ConsulHub
	region            *ConsulRegion
	broadcastChannels *ConsulRegionBroadcastChannels
}

// NewConsulConnection creates a new connection.
func NewConsulConnection(hub *ConsulHub, socket *websocket.Conn, consulRegion *ConsulRegion, channels *ConsulRegionBroadcastChannels) *ConsulConnection {
	connectionID := uuid.NewV4()

	return &ConsulConnection{
		ID:                connectionID,
		shortID:           fmt.Sprintf("%s", connectionID)[0:8],
		watches:           set.New(),
		hub:               hub,
		socket:            socket,
		receive:           make(chan *Action),
		send:              make(chan *Action),
		destroyCh:         make(chan struct{}),
		region:            consulRegion,
		broadcastChannels: channels,
	}
}

// Warningf is a stupid wrapper for logger.Warningf
func (c *ConsulConnection) Warningf(format string, args ...interface{}) {
	message := fmt.Sprintf("[%s] ", c.shortID) + format
	logger.Warningf(message, args...)
}

// Errorf is a stupid wrapper for logger.Errorf
func (c *ConsulConnection) Errorf(format string, args ...interface{}) {
	message := fmt.Sprintf("[%s] ", c.shortID) + format
	logger.Errorf(message, args...)
}

// Infof is a stupid wrapper for logger.Infof
func (c *ConsulConnection) Infof(format string, args ...interface{}) {
	message := fmt.Sprintf("[%s] ", c.shortID) + format
	logger.Infof(message, args...)
}

// Debugf is a stupid wrapper for logger.Debugf
func (c *ConsulConnection) Debugf(format string, args ...interface{}) {
	message := fmt.Sprintf("[%s] ", c.shortID) + format
	logger.Debugf(message, args...)
}

func (c *ConsulConnection) writePump() {
	defer func() {
		c.socket.Close()
	}()

	for {
		action, ok := <-c.send

		if !ok {
			if err := c.socket.WriteMessage(websocket.CloseMessage, []byte{}); err != nil {
				c.Errorf("Could not write close message to websocket: %s", err)
			}
			return
		}

		if err := c.socket.WriteJSON(action); err != nil {
			c.Errorf("Could not write action to websocket: %s", err)
		}
	}
}

func (c *ConsulConnection) readPump() {
	defer func() {
		c.watches.Clear()
		c.hub.unregister <- c
		c.socket.Close()
	}()

	// Register this connection with the hub for broadcast updates
	c.hub.register <- c

	var action Action
	for {
		err := c.socket.ReadJSON(&action)
		if err != nil {
			break
		}

		c.process(action)
	}
}

func (c *ConsulConnection) process(action Action) {
	c.Debugf("Processing event %s (index %d)", action.Type, action.Index)

	switch action.Type {

	//
	// Consul regions
	//
	case fetchConsulRegions:
		go c.fetchRegions()

	//
	// Consul services
	//
	case watchConsulServices:
		go c.watchGenericBroadcast("services", fetchedConsulServices, c.region.broadcastChannels.services, c.region.services)
	case unwatchConsulServices:
		c.unwatchGenericBroadcast("services")

	//
	// Consul service (single)
	//
	case watchConsulService:
		go c.watchConsulService(action)
	case unwatchConsulService:
		c.watches.Remove(action.Payload.(string))

	//
	// Consul nodes
	//
	case watchConsulNodes:
		go c.watchGenericBroadcast("nodes", fetchedConsulNodes, c.region.broadcastChannels.nodes, c.region.nodes)
	case unwatchConsulNodes:
		c.unwatchGenericBroadcast("nodes")

	//
	// Consul node (single)
	//
	case watchConsulNode:
		go c.watchConsulNode(action)
	case unwatchConsulNode:
		c.watches.Remove(action.Payload.(string))

	//
	// Nice in debug
	//
	default:
		logger.Warningf("Unknown action: %s", action.Type)
	}
}

// Handle monitors the websocket connection for incoming actions. It sends
// out actions on state changes.
func (c *ConsulConnection) Handle() {
	go c.writePump()
	c.readPump()

	c.Debugf("Connection closing down")

	c.destroyCh <- struct{}{}

	// Kill any remaining watcher routines
	close(c.destroyCh)
}

func (c *ConsulConnection) fetchRegions() {
	c.send <- &Action{Type: fetchedConsulRegions, Payload: c.hub.regions}
}

func (c *ConsulConnection) watchGenericBroadcast(watchKey string, actionEvent string, prop observer.Property, initialPayload interface{}) {
	if c.watches.Has(watchKey) {
		c.Warningf("Connection is already subscribed to %s", actionEvent)
		return
	}

	defer func() {
		c.watches.Remove(watchKey)
		c.Infof("Stopped watching %s", watchKey)

		// recovering from panic caused by writing to a closed channel
		if r := recover(); r != nil {
			c.Warningf("Recover from panic: %s", r)
		}
	}()

	c.watches.Add(watchKey)

	c.Debugf("Sending our current %s list", watchKey)
	c.send <- &Action{Type: actionEvent, Payload: initialPayload, Index: 0}

	stream := prop.Observe()

	c.Debugf("Started watching %s", watchKey)
	for {
		select {
		case <-c.destroyCh:
			return

		case <-stream.Changes():
			// advance to next value
			stream.Next()

			channelAction := stream.Value().(*Action)
			c.Debugf("got new data for %s (WaitIndex: %d)", watchKey, channelAction.Index)

			if !c.watches.Has(watchKey) {
				c.Infof("Connection is no longer subscribed to %s", watchKey)
				return
			}

			if channelAction.Type != actionEvent {
				c.Debugf("Type mismatch: %s <> %s", channelAction.Type, actionEvent)
				continue
			}

			c.Debugf("Publishing change %s %s", channelAction.Type, watchKey)
			c.send <- channelAction
		}
	}
}

func (c *ConsulConnection) unwatchGenericBroadcast(watchKey string) {
	c.Debugf("Removing subscription for %s", watchKey)
	c.watches.Remove(watchKey)
}

func (c *ConsulConnection) watchConsulService(action Action) {
	serviceID := action.Payload.(string)

	if c.watches.Has(serviceID) {
		c.Warningf("Connection is already subscribed to service %s", serviceID)
		return
	}

	defer func() {
		c.watches.Remove(serviceID)
		c.Infof("Stopped watching service with id: %s", serviceID)
	}()
	c.watches.Add(serviceID)

	c.Infof("Started watching service with id: %s", serviceID)

	q := &api.QueryOptions{WaitIndex: 1}
	for {
		select {
		case <-c.destroyCh:
			return

		default:
			service, meta, err := c.region.Client.Health().Service(serviceID, "", false, q)
			if err != nil {
				c.Errorf("connection: unable to fetch consul service info: %s", err)
				time.Sleep(10 * time.Second)
				continue
			}

			if !c.watches.Has(serviceID) {
				return
			}

			remoteWaitIndex := meta.LastIndex
			localWaitIndex := q.WaitIndex

			// only broadcast if the LastIndex has changed
			if remoteWaitIndex > localWaitIndex {
				c.send <- &Action{Type: fetchedConsulService, Payload: service, Index: remoteWaitIndex}
				q = &api.QueryOptions{WaitIndex: remoteWaitIndex, WaitTime: 10 * time.Second}

				// don't refresh data more frequent than every 5s, since busy clusters update every second or faster
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func (c *ConsulConnection) watchConsulNode(action Action) {
	nodeID := action.Payload.(string)

	if c.watches.Has(nodeID) {
		c.Warningf("Connection is already subscribed to node %s", nodeID)
		return
	}

	defer func() {
		c.watches.Remove(nodeID)
		c.Infof("Stopped watching node with id: %s", nodeID)
	}()
	c.watches.Add(nodeID)

	c.Infof("Started watching node with id: %s", nodeID)

	q := &api.QueryOptions{WaitIndex: 1}
	for {
		select {
		case <-c.destroyCh:
			return

		default:
			node, meta, err := c.region.Client.Health().Node(nodeID, q)
			if err != nil {
				c.Errorf("connection: unable to fetch consul node info: %s", err)
				time.Sleep(10 * time.Second)
				continue
			}

			if !c.watches.Has(nodeID) {
				return
			}

			remoteWaitIndex := meta.LastIndex
			localWaitIndex := q.WaitIndex

			// only broadcast if the LastIndex has changed
			if remoteWaitIndex == localWaitIndex {
				time.Sleep(5 * time.Second)
				continue
			}

			c.send <- &Action{Type: fetchedConsulNode, Payload: node, Index: remoteWaitIndex}
			q = &api.QueryOptions{WaitIndex: remoteWaitIndex, WaitTime: 10 * time.Second}

			// don't refresh data more frequent than every 5s, since busy clusters update every second or faster
			time.Sleep(5 * time.Second)
		}
	}
}
