// Copyright 2015-2017 HenryLee. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package teleport

import (
	"net/url"
	"reflect"
	"time"

	"github.com/henrylee2cn/goutil"

	"github.com/henrylee2cn/teleport/socket"
)

type (
	// PullCtx request handler context.
	// For example:
	//  type Home struct{ PullCtx }
	PullCtx interface {
		PushCtx
		SetBodyCodec(string)
	}
	// PushCtx push handler context.
	// For example:
	//  type Home struct{ PushCtx }
	PushCtx interface {
		Uri() string
		Path() string
		Query() url.Values
		Public() goutil.Map
		PublicLen() int
		Ip() string
		Peer() *Peer
		Session() *Session
	}
	// ApiContext the underlying common instance of PullCtx and PushCtx.
	ApiContext struct {
		session           *Session
		input             *socket.Packet
		output            *socket.Packet
		apiType           *Handler
		originStructMaker func(*ApiContext) reflect.Value
		method            reflect.Method
		arg               reflect.Value
		pullCmd           *PullCmd
		uri               *url.URL
		query             url.Values
		public            goutil.Map
		start             time.Time
		cost              time.Duration
		next              *ApiContext
	}
)

var (
	_ PullCtx = new(ApiContext)
	_ PushCtx = new(ApiContext)
)

// newApiContext creates a ApiContext for one request/response or push.
func newApiContext() *ApiContext {
	c := new(ApiContext)
	c.input = socket.NewPacket(c.binding)
	c.output = socket.NewPacket(nil)
	return c
}

func (c *ApiContext) reInit(s *Session) {
	c.session = s
	c.public = goutil.RwMap()
	if s.socket.PublicLen() > 0 {
		s.socket.Public().Range(func(key, value interface{}) bool {
			c.public.Store(key, value)
			return true
		})
	}
}

var (
	emptyValue  = reflect.Value{}
	emptyMethod = reflect.Method{}
)

func (c *ApiContext) clean() {
	c.session = nil
	c.apiType = nil
	c.arg = emptyValue
	c.originStructMaker = nil
	c.method = emptyMethod
	c.pullCmd = nil
	c.public = nil
	c.uri = nil
	c.query = nil
	c.cost = 0
	c.input.Reset(c.binding)
	c.output.Reset(nil)
}

// Peer returns the peer.
func (c *ApiContext) Peer() *Peer {
	return c.session.peer
}

// Session returns the session.
func (c *ApiContext) Session() *Session {
	return c.session
}

// Public returns temporary public data of Conn Context.
func (c *ApiContext) Public() goutil.Map {
	return c.public
}

// PublicLen returns the length of public data of Conn Context.
func (c *ApiContext) PublicLen() int {
	return c.public.Len()
}

// Uri returns the input packet uri.
func (c *ApiContext) Uri() string {
	return c.input.Header.Uri
}

// Path returns the input packet uri path.
func (c *ApiContext) Path() string {
	return c.uri.Path
}

// Query returns the input packet uri query.
func (c *ApiContext) Query() url.Values {
	if c.query == nil {
		c.query = c.uri.Query()
	}
	return c.query
}

// SetBodyCodec sets the body codec for response packet.
func (c *ApiContext) SetBodyCodec(codecName string) {
	c.output.BodyCodec = codecName
}

// Ip returns the remote addr.
func (c *ApiContext) Ip() string {
	return c.session.RemoteIp()
}

func (c *ApiContext) binding(header *socket.Header) interface{} {
	c.start = time.Now()
	switch header.Type {
	case TypePullReply:
		return c.bindPullReply(header)

	case TypePush:
		return c.bindPush(header)

	case TypePull:
		return c.bindPull(header)

	default:
		return nil
	}
}

func (c *ApiContext) bindPush(header *socket.Header) interface{} {
	var err error
	c.uri, err = url.Parse(header.Uri)
	if err != nil {
		return nil
	}
	var ok bool
	c.apiType, ok = c.session.pushRouter.get(c.Path())
	if !ok {
		return nil
	}
	c.originStructMaker = c.apiType.originStructMaker
	c.arg = reflect.New(c.apiType.arg)
	return c.arg.Interface()
}

func (c *ApiContext) bindPull(header *socket.Header) interface{} {
	c.output.Header.Seq = c.input.Header.Seq
	c.output.Header.Type = TypePullReply
	c.output.Header.Uri = c.input.Header.Uri
	c.output.HeaderCodec = c.input.HeaderCodec
	c.output.Header.Gzip = c.input.Header.Gzip

	var err error
	c.uri, err = url.Parse(header.Uri)
	if err != nil {
		c.output.Header.StatusCode = StatusBadPull
		c.output.Header.Status = err.Error()
		return nil
	}
	var ok bool
	c.apiType, ok = c.session.requestRouter.get(c.Path())
	if !ok {
		c.output.Header.StatusCode = StatusNotFound
		c.output.Header.Status = StatusText(StatusNotFound)
		return nil
	}
	c.originStructMaker = c.apiType.originStructMaker
	c.arg = reflect.New(c.apiType.arg)
	return c.arg.Interface()
}

// handle handles and replies pull, or handles push.
func (c *ApiContext) handle() {
	defer func() {
		c.cost = time.Since(c.start)
		c.session.runlog(c.cost, c.input, c.output)
	}()

	if c.output.Header.StatusCode != StatusNotFound {
		rets := c.apiType.method.Func.Call([]reflect.Value{c.originStructMaker(c), c.arg})

		// receive push
		if len(rets) == 0 {
			return
		}

		c.output.Body = rets[0].Interface()
		e, ok := rets[1].Interface().(Xerror)
		if !ok || e == nil {
			c.output.Header.StatusCode = StatusOK
			c.output.Header.Status = StatusText(StatusOK)
		} else {
			c.output.Header.StatusCode = e.Code()
			c.output.Header.Status = e.Text()
		}
	}

	// reply pull
	if len(c.output.BodyCodec) == 0 {
		c.output.BodyCodec = c.input.BodyCodec
	}

	err := c.session.write(c.output)
	if err != nil {
		Debugf("WritePacket: %s", err.Error())
	}
}

func (c *ApiContext) bindPullReply(header *socket.Header) interface{} {
	pullCmd, ok := c.session.pullCmdMap.Load(header.Seq)
	if !ok {
		return nil
	}
	c.session.pullCmdMap.Delete(header.Seq)
	c.pullCmd = pullCmd.(*PullCmd)
	return c.pullCmd.reply
}

// pullHandle receives pull reply.
func (c *ApiContext) pullReplyHandle() {
	c.pullCmd.done()
	c.pullCmd.cost = time.Since(c.pullCmd.start)
	c.session.runlog(c.pullCmd.cost, c.input, c.pullCmd.output)
}
