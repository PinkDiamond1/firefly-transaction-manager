// Copyright © 2022 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hyperledger/firefly-common/pkg/config"
	"github.com/hyperledger/firefly-common/pkg/fftypes"
	"github.com/hyperledger/firefly-transaction-manager/internal/confirmations"
	"github.com/hyperledger/firefly-transaction-manager/internal/tmconfig"
	"github.com/hyperledger/firefly-transaction-manager/mocks/confirmationsmocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/ffcapimocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/persistencemocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/wsmocks"
	"github.com/hyperledger/firefly-transaction-manager/pkg/apitypes"
	"github.com/hyperledger/firefly-transaction-manager/pkg/ffcapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func strPtr(s string) *string { return &s }

func testESConf(t *testing.T, j string) (spec *apitypes.EventStream) {
	err := json.Unmarshal([]byte(j), &spec)
	assert.NoError(t, err)
	spec.ID = apitypes.UUIDVersion1()
	return spec
}

func newTestEventStream(t *testing.T, conf string) (es *eventStream) {
	es, err := newTestEventStreamWithListener(t, &ffcapimocks.API{}, conf)
	assert.NoError(t, err)
	return es
}

func newTestEventStreamWithListener(t *testing.T, mfc *ffcapimocks.API, conf string, listeners ...*apitypes.Listener) (es *eventStream, err error) {
	tmconfig.Reset()
	config.Set(tmconfig.EventStreamsDefaultsBatchTimeout, "1us")
	InitDefaults()
	ees, err := NewEventStream(context.Background(), testESConf(t, conf),
		mfc,
		&persistencemocks.Persistence{},
		&wsmocks.WebSocketChannels{},
		listeners,
	)
	if err != nil {
		return nil, err
	}
	es = ees.(*eventStream)
	mcm := &confirmationsmocks.Manager{}
	es.confirmations = mcm
	mcm.On("Start").Return(nil).Maybe()
	mcm.On("Stop").Return(nil).Maybe()
	mcm.On("Notify", mock.Anything).Run(func(args mock.Arguments) {
		n := args[0].(*confirmations.Notification)
		if n.Event != nil {
			go n.Event.Confirmed([]confirmations.BlockInfo{})
		}
	}).Return(nil).Maybe()
	return es, err
}

func mockWSChannels(wsc *wsmocks.WebSocketChannels) (chan interface{}, chan interface{}, chan error) {
	senderChannel := make(chan interface{}, 1)
	broadcastChannel := make(chan interface{}, 1)
	receiverChannel := make(chan error, 1)
	wsc.On("GetChannels", "ut_stream").Return((chan<- interface{})(senderChannel), (chan<- interface{})(broadcastChannel), (<-chan error)(receiverChannel))
	return senderChannel, broadcastChannel, receiverChannel
}

func TestNewTestEventStreamMissingID(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()
	_, err := NewEventStream(context.Background(), &apitypes.EventStream{},
		&ffcapimocks.API{},
		&persistencemocks.Persistence{},
		&wsmocks.WebSocketChannels{},
		[]*apitypes.Listener{},
	)
	assert.Regexp(t, "FF21048", err)
}

func TestNewTestEventStreamBadConfig(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()
	_, err := NewEventStream(context.Background(), testESConf(t, `{}`),
		&ffcapimocks.API{},
		&persistencemocks.Persistence{},
		&wsmocks.WebSocketChannels{},
		[]*apitypes.Listener{},
	)
	assert.Regexp(t, "FF21028", err)
}

func TestConfigNewDefaultsUpdate(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()

	es := testESConf(t, `{
		"name": "test1"
	}`)
	es, changed, err := mergeValidateEsConfig(context.Background(), nil, es)
	assert.NoError(t, err)
	assert.True(t, changed)

	b, err := json.Marshal(&es)
	assert.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"`+es.ID.String()+`",
		"created":"`+es.Created.String()+`",
		"updated":"`+es.Created.String()+`",
		"batchSize": 50,
		"batchTimeout": "5s",
		"blockedRetryDelay": "30s",
		"errorHandling":"block",
		"name":"test1",
		"retryTimeout":"30s",
		"suspended":false,
		"type":"websocket",
		"websocket": {
			"distributionMode":"load_balance"
		}
	}`, string(b))

	es, changed, err = mergeValidateEsConfig(context.Background(), es, es)
	assert.NoError(t, err)
	assert.False(t, changed)

	es2, changed, err := mergeValidateEsConfig(context.Background(), es, testESConf(t, `{
		"id": "4023945d-ea5d-43aa-ab4f-f39f8c055c7e",`+ /* ignored */ `
		"batchSize": 111,
		"batchTimeoutMS": 222,
		"blockedRetryDelaySec": 333,
		"errorHandling": "skip",
		"name": "test2",
		"retryTimeoutSec": 444,
		"suspended": true,
		"type": "webhook",
		"webhook": {
			"url": "http://test.example.com"
		}
	}`))

	assert.NoError(t, err)
	assert.True(t, changed)

	b, err = json.Marshal(&es2)
	assert.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"`+es.ID.String()+`",
		"created":"`+es.Created.String()+`",
		"updated":"`+es2.Updated.String()+`",
		"batchSize": 111,
		"batchTimeout": "222ms",
		"blockedRetryDelay": "5m33s",
		"errorHandling":"skip",
		"name":"test2",
		"retryTimeout":"7m24s",
		"suspended":true,
		"type":"webhook",
		"webhook": {
			"tlsSkipHostVerify": false,
			"requestTimeout": "30s",
			"url": "http://test.example.com"
		}
	}`, string(b))

}

func TestConfigNewMissingWebhookConf(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()

	_, _, err := mergeValidateEsConfig(context.Background(), nil, testESConf(t, `{
		"name": "test",
		"type": "webhook",
		"websocket": {}
	}`))
	assert.Regexp(t, "FF21030", err)

}

func TestConfigBadWebSocketDistModeConf(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()

	_, _, err := mergeValidateEsConfig(context.Background(), nil, testESConf(t, `{
		"name": "test",
		"type": "websocket",
		"websocket": {
			"distributionMode":"wrong"
		}
	}`))
	assert.Regexp(t, "FF21034", err)

}

func TestConfigWebSocketBroadcast(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()

	es, _, err := mergeValidateEsConfig(context.Background(), nil, testESConf(t, `{
		"name": "test",
		"type": "websocket",
		"websocket": {
			"distributionMode":"broadcast"
		}
	}`))
	assert.NoError(t, err)
	assert.Equal(t, apitypes.DistributionModeBroadcast, *es.WebSocket.DistributionMode)

}

func TestConfigNewBadType(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()

	_, _, err := mergeValidateEsConfig(context.Background(), nil, testESConf(t, `{
		"name": "test",
		"type": "wrong"
	}`))
	assert.Regexp(t, "FF21029", err)

}

func TestConfigNewWebhookRetryMigration(t *testing.T) {
	tmconfig.Reset()
	InitDefaults()

	es, changed, err := mergeValidateEsConfig(context.Background(), nil, testESConf(t, `{
		"name": "test",
		"type": "webhook",
		"webhook": {
			"urL": "http://www.example.com",
			"requestTimeoutSec": 5
		}
	}`))
	assert.NoError(t, err)
	assert.True(t, changed)

	assert.Equal(t, fftypes.FFDuration(5*time.Second), *es.Webhook.RequestTimeout)

}

func TestInitActionBadAction(t *testing.T) {
	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)
	badType := apitypes.EventStreamType("wrong")
	es.spec.Type = &badType
	assert.Panics(t, func() {
		es.initAction(&startedStreamState{
			ctx: context.Background(),
		})
	})
}

func TestWebSocketEventStreamsE2EMigrationThenStart(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	addr := "0x12345"
	l := &apitypes.Listener{
		ID:                apitypes.UUIDVersion1(),
		Name:              strPtr("ut_listener"),
		DeprecatedAddress: &addr,
		DeprecatedEvent:   fftypes.JSONAnyPtr(`{"event":"definition"}`),
		Options:           fftypes.JSONAnyPtr(`{"option1":"value1"}`),
		FromBlock:         strPtr("12345"),
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.MatchedBy(func(req *ffcapi.EventListenerVerifyOptionsRequest) bool {
		return req.FromBlock == "12345" && req.Options.JSONObject().GetString("option1") == "value1"
	})).Return(&ffcapi.EventListenerVerifyOptionsResponse{
		ResolvedSignature: "EventSig(uint256)",
		ResolvedOptions:   *fftypes.JSONAnyPtr(`{"option1":"value1","option2":"value2"}`),
	}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventStreamStartRequest)
		started <- r
		assert.Len(t, r.InitialListeners, 1)
		assert.JSONEq(t, `{
			"event": {"event":"definition"},
			"address": "0x12345"
		}`, r.InitialListeners[0].Filters[0].String())
		assert.JSONEq(t, `{
			"option1":"value1",
			"option2":"value2"
		}`, r.InitialListeners[0].Options.String())
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil)

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("WriteCheckpoint", mock.Anything, mock.MatchedBy(func(cp *apitypes.EventStreamCheckpoint) bool {
		return cp.StreamID.Equals(es.spec.ID) && cp.Listeners[*l.ID].JSONObject().GetString("cp1data") == "stuff"
	})).Return(nil)

	senderChannel, _, receiverChannel := mockWSChannels(es.wsChannels.(*wsmocks.WebSocketChannels))

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	assert.Equal(t, apitypes.EventStreamStatusStarted, es.Status())

	err = es.Start(es.bgCtx) // double start is error
	assert.Regexp(t, "FF21027", err)

	r := <-started

	r.EventStream <- &ffcapi.ListenerEvent{
		Checkpoint: fftypes.JSONAnyPtr(`{"cp1data": "stuff"}`),
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID:       l.ID,
				BlockNumber:      42,
				TransactionIndex: 13,
				LogIndex:         1,
			},
			Data: fftypes.JSONAnyPtr(`{"k1":"v1"}`),
			Info: fftypes.JSONAnyPtr(`{"blockNumber":"42","transactionIndex":"13","logIndex":"1"}`),
		},
	}

	batch1 := (<-senderChannel).([]*ffcapi.EventWithContext)
	assert.Len(t, batch1, 1)
	assert.Equal(t, "v1", batch1[0].Data.JSONObject().GetString("k1"))

	receiverChannel <- nil // ack

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestWebhookEventStreamsE2EAddAfterStart(t *testing.T) {

	receivedWebhook := make(chan []*ffcapi.EventWithContext, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/test/path", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("content-type"))
		var events []*ffcapi.EventWithContext
		err := json.NewDecoder(r.Body).Decode(&events)
		assert.NoError(t, err)
		receivedWebhook <- events
	}))
	defer s.Close()

	es := newTestEventStream(t, `{
		"name": "ut_stream",
		"type": "webhook",
		"webhook": {
			"url": "`+fmt.Sprintf("http://%s/test/path", s.Listener.Addr())+`"
		}
	}`)

	l := &apitypes.Listener{
		ID: fftypes.NewUUID(),
		Filters: []fftypes.JSONAny{
			`{"event":"definition1"}`,
			`{"event":"definition2"}`,
		},
		Options:   fftypes.JSONAnyPtr(`{"option1":"value1"}`),
		FromBlock: strPtr("12345"),
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.MatchedBy(func(req *ffcapi.EventListenerVerifyOptionsRequest) bool {
		return req.FromBlock == "12345" && req.Options.JSONObject().GetString("option1") == "value1"
	})).Return(&ffcapi.EventListenerVerifyOptionsResponse{
		ResolvedSignature: "EventSig(uint256)",
		ResolvedOptions:   *fftypes.JSONAnyPtr(`{"option1":"value1","option2":"value2"}`),
	}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventStreamStartRequest)
		started <- r
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerAdd", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerAddRequest) bool {
		return r.ID.Equals(l.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventListenerAddRequest)
		assert.JSONEq(t, `{"event":"definition1"}`, r.Filters[0].String())
		assert.JSONEq(t, `{"event":"definition2"}`, r.Filters[1].String())
		assert.JSONEq(t, `{
				"option1":"value1",
				"option2":"value2"
			}`, r.Options.String())
	}).Return(&ffcapi.EventListenerAddResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil)

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("WriteCheckpoint", mock.Anything, mock.MatchedBy(func(cp *apitypes.EventStreamCheckpoint) bool {
		return cp.StreamID.Equals(es.spec.ID) && cp.Listeners[*l.ID].JSONObject().GetString("cp1data") == "stuff"
	})).Return(nil)

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	l, err = es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)
	assert.Equal(t, "EventSig(uint256)", *l.Name) // Defaulted

	r := <-started

	r.EventStream <- &ffcapi.ListenerEvent{
		Checkpoint: fftypes.JSONAnyPtr(`{"cp1data": "stuff"}`),
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID:       l.ID,
				BlockNumber:      42,
				TransactionIndex: 13,
				LogIndex:         1,
			},
			Data: fftypes.JSONAnyPtr(`{"k1":"v1"}`),
			Info: fftypes.JSONAnyPtr(`{"blockNumber":"42","transactionIndex":"13","logIndex":"1"}`),
		},
	}

	batch1 := <-receivedWebhook
	assert.Len(t, batch1, 1)
	assert.Equal(t, "v1", batch1[0].Data.JSONObject().GetString("k1"))

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestConnectorRejectListener(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`badness`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("pop"))

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.Regexp(t, "FF21040.*pop", err)

	mfc.AssertExpectations(t)
}

func TestStartWithExistingStreamOk(t *testing.T) {

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := &ffcapimocks.API{}

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	_, err := newTestEventStreamWithListener(t, mfc, `{
		"name": "ut_stream"
	}`, l)
	assert.NoError(t, err)

	mfc.AssertExpectations(t)
}

func TestStartWithExistingStreamFail(t *testing.T) {

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := &ffcapimocks.API{}

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), fmt.Errorf("pop"))

	_, err := newTestEventStreamWithListener(t, mfc, `{
		"name": "ut_stream"
	}`, l)
	assert.Regexp(t, "pop", err)

	mfc.AssertExpectations(t)
}

func TestUpdateStreamStarted(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventStreamStartRequest)
		started <- r
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil).Twice()

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	r := <-started

	defNoChange := testESConf(t, `{
		"name": "ut_stream"
	}`)
	err = es.UpdateSpec(context.Background(), defNoChange)
	assert.NoError(t, err)

	defChanged := testESConf(t, `{
		"name": "ut_stream2"
	}`)
	err = es.UpdateSpec(context.Background(), defChanged)
	assert.NoError(t, err)

	assert.Equal(t, "ut_stream2", *es.Spec().Name)

	<-r.StreamContext.Done()
	r = <-started

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestAddRemoveListener(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventStreamStartRequest)
		started <- r
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil).Once()

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	r := <-started

	err = es.RemoveListener(es.bgCtx, l.ID)
	assert.NoError(t, err)

	err = es.RemoveListener(es.bgCtx, l.ID)
	assert.NoError(t, err)

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestUpdateListenerAndDeleteStarted(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l1 := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventStreamStartRequest)
		started <- r
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerAdd", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerAddRequest) bool {
		return r.ID.Equals(l1.ID)
	})).Return(&ffcapi.EventListenerAddResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l1.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil).Twice()

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("GetCheckpoint", mock.Anything, es.spec.ID).Return(nil, nil)
	msp.On("DeleteCheckpoint", mock.Anything, es.spec.ID).Return(fmt.Errorf("pop")).Once()
	msp.On("DeleteCheckpoint", mock.Anything, es.spec.ID).Return(nil)

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	_, err = es.AddOrUpdateListener(es.bgCtx, l1.ID, l1, false)
	assert.NoError(t, err)

	r := <-started

	// Double add the same - no change, already started
	_, err = es.AddOrUpdateListener(es.bgCtx, l1.ID, l1, false)
	assert.NoError(t, err)

	updates := &apitypes.Listener{
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition2"}`},
	}

	// Change the event definition, with reset
	_, err = es.AddOrUpdateListener(es.bgCtx, l1.ID, updates, true)
	assert.NoError(t, err)

	r = <-started

	err = es.Delete(es.bgCtx)
	assert.Regexp(t, "pop", err)

	err = es.Delete(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestUpdateListenerFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l1 := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		r := args[1].(*ffcapi.EventStreamStartRequest)
		started <- r
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil)

	mfc.On("EventListenerRemove", mock.Anything, mock.Anything).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("pop")).Once()
	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l1.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil).Once()

	_, err := es.AddOrUpdateListener(es.bgCtx, l1.ID, l1, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	r := <-started

	// Double add the same
	_, err = es.AddOrUpdateListener(es.bgCtx, l1.ID, l1, false)
	assert.NoError(t, err)

	updates := &apitypes.Listener{
		Name:      strPtr("ut_listener"),
		FromBlock: strPtr("0"),
	}

	// Update and reset
	_, err = es.AddOrUpdateListener(es.bgCtx, l1.ID, updates, true)
	assert.Regexp(t, "pop", err)

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestUpdateEventStreamBad(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "old_name"
	}`)

	defNoChange := testESConf(t, `{
		"name": "new_name",
		"type": "wrong"
	}`)
	err := es.UpdateSpec(context.Background(), defNoChange)
	assert.Regexp(t, "FF21029", err)

	assert.Equal(t, "old_name", *es.Spec().Name)

}

func TestUpdateStreamRestartFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		started <- args[1].(*ffcapi.EventStreamStartRequest)
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()

	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		started <- args[1].(*ffcapi.EventStreamStartRequest)
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), fmt.Errorf("pop")).Once()

	mfc.On("EventListenerAdd", mock.Anything, mock.Anything).Return(nil, ffcapi.ErrorReason(""), nil).Once()

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil)

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	_, err = es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	r := <-started

	defChanged := testESConf(t, `{
		"name": "ut_stream2"
	}`)
	err = es.UpdateSpec(context.Background(), defChanged)
	assert.Regexp(t, "FF21032.*pop", err)

	<-r.StreamContext.Done()

	assert.Equal(t, apitypes.EventStreamStatusStopped, es.status)

	mfc.AssertExpectations(t)
}

func TestUpdateAttemptChangeSignature(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{
		ResolvedSignature: "sig1",
	}, ffcapi.ErrorReason(""), nil).Once()
	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{
		ResolvedSignature: "sig2",
	}, ffcapi.ErrorReason(""), nil).Once()

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		started <- args[1].(*ffcapi.EventStreamStartRequest)
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil).Once()

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	// Attempt to update filters
	_, err = es.AddOrUpdateListener(es.bgCtx, l.ID, &apitypes.Listener{
		Filters: []fftypes.JSONAny{*fftypes.JSONAnyPtr(`{"new":"filter"}`)},
	}, false)
	assert.Regexp(t, "FF21051", err)

	r := <-started

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestAttemptResetNonExistentListener(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, true)
	assert.Regexp(t, "FF21052", err)

	mfc.AssertExpectations(t)

}

func TestUpdateStreamStopFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		started <- args[1].(*ffcapi.EventStreamStartRequest)
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()

	mfc.On("EventListenerRemove", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), fmt.Errorf("pop")).Twice()

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil).Once()

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	r := <-started

	defChanged := testESConf(t, `{
		"name": "ut_stream2"
	}`)
	err = es.UpdateSpec(context.Background(), defChanged)
	assert.Regexp(t, "FF21031.*pop", err)

	err = es.Delete(context.Background())
	assert.Regexp(t, "pop", err)

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

	<-r.StreamContext.Done()

	mfc.AssertExpectations(t)
}

func TestResetListenerRestartFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		started <- args[1].(*ffcapi.EventStreamStartRequest)
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()
	mfc.On("EventStreamStart", mock.Anything, mock.Anything).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), fmt.Errorf("pop")).Once()

	mfc.On("EventListenerAdd", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerAddResponse{}, ffcapi.ErrorReason(""), nil).Once()

	mfc.On("EventListenerRemove", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventListenerRemoveRequest) bool {
		return r.ID.Equals(l.ID)
	})).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil)

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("GetCheckpoint", mock.Anything, es.spec.ID).Return(&apitypes.EventStreamCheckpoint{
		StreamID:  es.spec.ID,
		Listeners: make(map[fftypes.UUID]*fftypes.JSONAny),
	}, nil)
	msp.On("WriteCheckpoint", mock.Anything, mock.Anything).Return(nil)
	msp.On("DeleteCheckpoint", mock.Anything, es.spec.ID).Return(nil)

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	_, err = es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	_, err = es.AddOrUpdateListener(es.bgCtx, l.ID, l, true)
	assert.Regexp(t, "pop", err)

	err = es.Delete(es.bgCtx)
	assert.NoError(t, err)

	mfc.AssertExpectations(t)
}

func TestResetListenerWriteCheckpointFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	l := &apitypes.Listener{
		ID:      fftypes.NewUUID(),
		Name:    strPtr("ut_listener"),
		Filters: []fftypes.JSONAny{`{"event":"definition1"}`},
	}

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("GetCheckpoint", mock.Anything, es.spec.ID).Return(&apitypes.EventStreamCheckpoint{
		StreamID:  es.spec.ID,
		Listeners: make(map[fftypes.UUID]*fftypes.JSONAny),
	}, nil)
	msp.On("WriteCheckpoint", mock.Anything, mock.Anything).Return(fmt.Errorf("pop"))

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	_, err = es.AddOrUpdateListener(es.bgCtx, l.ID, l, true)
	assert.Regexp(t, "pop", err)

	mfc.AssertExpectations(t)
}

func TestStopWhenNotStarted(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	err := es.Stop(es.bgCtx)
	assert.Regexp(t, "FF21027", err)

}

func TestWebSocketBroadcastActionCloseDuringCheckpoint(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream",
		"websocket": {
			"distributionMode": "broadcast"
		}
	}`)

	l := &apitypes.Listener{
		ID:        fftypes.NewUUID(),
		Name:      strPtr("ut_listener"),
		Filters:   []fftypes.JSONAny{`{"event":"definition1"}`},
		Options:   fftypes.JSONAnyPtr(`{"option1":"value1"}`),
		FromBlock: strPtr("12345"),
	}

	mfc := es.connector.(*ffcapimocks.API)

	mfc.On("EventListenerVerifyOptions", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerVerifyOptionsResponse{}, ffcapi.ErrorReason(""), nil)

	started := make(chan *ffcapi.EventStreamStartRequest, 1)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Run(func(args mock.Arguments) {
		started <- args[1].(*ffcapi.EventStreamStartRequest)
	}).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()
	mfc.On("EventListenerRemove", mock.Anything, mock.Anything).Return(&ffcapi.EventListenerRemoveResponse{}, ffcapi.ErrorReason(""), nil)

	first := true
	done := make(chan struct{})
	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("WriteCheckpoint", mock.Anything, mock.Anything).Return(fmt.Errorf("pop")).Run(func(args mock.Arguments) {
		if first {
			go func() {
				// Close here so we exit the loop
				err := es.Stop(es.bgCtx)
				assert.NoError(t, err)
				close(done)
			}()
			first = false
		}
	})

	_, broadcastChannel, _ := mockWSChannels(es.wsChannels.(*wsmocks.WebSocketChannels))

	_, err := es.AddOrUpdateListener(es.bgCtx, l.ID, l, false)
	assert.NoError(t, err)

	err = es.Start(es.bgCtx)
	assert.NoError(t, err)

	r := <-started

	r.EventStream <- &ffcapi.ListenerEvent{
		Checkpoint: fftypes.JSONAnyPtr(`{"cp1data": "stuff"}`),
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID:       l.ID,
				BlockNumber:      42,
				TransactionIndex: 13,
				LogIndex:         1,
			},
			Data: fftypes.JSONAnyPtr(`{"k1":"v1"}`),
			Info: fftypes.JSONAnyPtr(`{"blockNumber":"42","transactionIndex":"13","logIndex":"1"}`),
		},
	}
	batch1 := (<-broadcastChannel).([]*ffcapi.EventWithContext)
	assert.Len(t, batch1, 1)
	assert.Equal(t, "v1", batch1[0].Data.JSONObject().GetString("k1"))

	<-r.StreamContext.Done()
	<-done

	mfc.AssertExpectations(t)
}

func TestActionRetryOk(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream",
		"errorHandling": "skip",
		"retryTimeout": "1s"
	}`)

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	es.mux.Lock()
	callCount := 0
	es.currentState.action = func(ctx context.Context, batchNumber, attempt int, events []*ffcapi.EventWithContext) error {
		callCount++
		if callCount > 1 {
			return nil
		}
		return fmt.Errorf("pop")
	}
	es.mux.Unlock()

	// No-op
	err = es.performActionsWithRetry(es.currentState, &eventStreamBatch{})
	assert.NoError(t, err)

	// retry then ok
	err = es.performActionsWithRetry(es.currentState, &eventStreamBatch{
		events: []*ffcapi.EventWithContext{
			{StreamID: es.spec.ID},
		},
	})
	assert.NoError(t, err)

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

}

func TestActionRetrySkip(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream",
		"errorHandling": "skip",
		"blockedRetryDelay": "0s",
		"retryTimeout": "0s"
	}`)

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	es.mux.Lock()
	es.currentState.action = func(ctx context.Context, batchNumber, attempt int, events []*ffcapi.EventWithContext) error {
		return fmt.Errorf("pop")
	}
	es.mux.Unlock()

	// Skip behavior
	err = es.performActionsWithRetry(es.currentState, &eventStreamBatch{
		events: []*ffcapi.EventWithContext{
			{StreamID: es.spec.ID},
		},
	})
	assert.NoError(t, err)

	err = es.Stop(es.bgCtx)
	assert.NoError(t, err)

}

func TestActionRetryBlock(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream",
		"errorHandling": "block",
		"blockedRetryDelay": "0s",
		"retryTimeout": "0s"
	}`)

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventStreamStart", mock.Anything, mock.MatchedBy(func(r *ffcapi.EventStreamStartRequest) bool {
		return r.ID.Equals(es.spec.ID)
	})).Return(&ffcapi.EventStreamStartResponse{}, ffcapi.ErrorReason(""), nil).Once()

	err := es.Start(es.bgCtx)
	assert.NoError(t, err)

	es.mux.Lock()
	callCount := 0
	done := make(chan struct{})
	es.currentState.action = func(ctx context.Context, batchNumber, attempt int, events []*ffcapi.EventWithContext) error {
		callCount++
		if callCount == 1 {
			go func() {
				err = es.Stop(es.bgCtx)
				assert.NoError(t, err)
				close(done)
			}()
		}
		return fmt.Errorf("pop")
	}
	es.mux.Unlock()

	// Skip behavior
	err = es.performActionsWithRetry(es.currentState, &eventStreamBatch{
		events: []*ffcapi.EventWithContext{
			{StreamID: es.spec.ID},
		},
	})
	assert.Regexp(t, "FF00154", err)

	<-done
	assert.Greater(t, callCount, 0)
}

func TestEventLoopProcessRemovedEvent(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		eventLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	u1 := &ffcapi.ListenerEvent{
		Removed: true,
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID: fftypes.NewUUID(),
			},
		},
	}
	mcm := &confirmationsmocks.Manager{}
	mcm.On("Notify", mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		ss.cancelCtx()
	})
	es.confirmations = mcm
	es.listeners[*u1.Event.ListenerID] = &listener{
		spec: &apitypes.Listener{ID: u1.Event.ListenerID},
	}

	go func() {
		ss.updates <- u1
	}()

	es.eventLoop(ss)
}

func TestEventLoopProcessRemovedEventFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		eventLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	u1 := &ffcapi.ListenerEvent{
		Removed: true,
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID: fftypes.NewUUID(),
			},
		},
	}
	mcm := &confirmationsmocks.Manager{}
	mcm.On("Notify", mock.Anything).Return(fmt.Errorf("pop")).Run(func(args mock.Arguments) {
		ss.cancelCtx()
	})
	es.confirmations = mcm
	es.listeners[*u1.Event.ListenerID] = &listener{
		spec: &apitypes.Listener{ID: u1.Event.ListenerID},
	}

	go func() {
		ss.updates <- u1
	}()

	es.eventLoop(ss)
}

func TestEventLoopConfirmationsManagerBypass(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		eventLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	u1 := &ffcapi.ListenerEvent{
		Checkpoint: fftypes.JSONAnyPtr("{}"),
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID: fftypes.NewUUID(),
			},
		},
	}
	es.confirmations = nil
	es.listeners[*u1.Event.ListenerID] = &listener{
		spec: &apitypes.Listener{ID: u1.Event.ListenerID},
	}

	go func() {
		ss.updates <- u1
		u2 := <-es.batchChannel
		assert.Equal(t, u1, u2)
		ss.cancelCtx()
	}()

	es.eventLoop(ss)
}

func TestEventLoopConfirmationsManagerFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		eventLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	u1 := &ffcapi.ListenerEvent{
		Checkpoint: fftypes.JSONAnyPtr("{}"),
		Event: &ffcapi.Event{
			EventID: ffcapi.EventID{
				ListenerID: fftypes.NewUUID(),
			},
		},
	}
	mcm := &confirmationsmocks.Manager{}
	mcm.On("Notify", mock.Anything).Return(fmt.Errorf("pop")).Run(func(args mock.Arguments) {
		ss.cancelCtx()
	})
	es.confirmations = mcm
	es.listeners[*u1.Event.ListenerID] = &listener{
		spec: &apitypes.Listener{ID: u1.Event.ListenerID},
	}

	go func() {
		ss.updates <- u1
	}()

	es.eventLoop(ss)
}

func TestEventLoopIgnoreBadEvent(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	es.processNewEvent(context.Background(), &ffcapi.ListenerEvent{})
}

func TestHWMCheckpointAfterInactivity(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		batchLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	li := &listener{
		spec: &apitypes.Listener{ID: fftypes.NewUUID()},
	}

	mcm := &confirmationsmocks.Manager{}
	mcm.On("CheckInFlight", li.spec.ID).Return(false)
	es.confirmations = mcm
	es.listeners[*li.spec.ID] = li

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventListenerHWM", mock.Anything, mock.MatchedBy(func(req *ffcapi.EventListenerHWMRequest) bool {
		return req.StreamID.Equals(es.spec.ID) && req.ListenerID.Equals(li.spec.ID)
	})).Run(func(args mock.Arguments) {
		ss.cancelCtx()
	}).Return(&ffcapi.EventListenerHWMResponse{Checkpoint: *fftypes.JSONAnyPtr(`{"cp1data":"stuff"}`)}, ffcapi.ErrorReason(""), nil)

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("WriteCheckpoint", mock.Anything, mock.MatchedBy(func(cp *apitypes.EventStreamCheckpoint) bool {
		return cp.StreamID.Equals(es.spec.ID) && cp.Listeners[*li.spec.ID].JSONObject().GetString("cp1data") == "stuff"
	})).Return(nil)

	es.checkpointInterval = 1 * time.Microsecond

	es.batchLoop(ss)

	mfc.AssertExpectations(t)
	msp.AssertExpectations(t)
	mcm.AssertExpectations(t)
}

func TestHWMCheckpointInFlightSkip(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		batchLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	li := &listener{
		spec: &apitypes.Listener{ID: fftypes.NewUUID()},
	}

	mcm := &confirmationsmocks.Manager{}
	mcm.On("CheckInFlight", li.spec.ID).Run(func(args mock.Arguments) {
		ss.cancelCtx()
	}).Return(true)
	es.confirmations = mcm
	es.listeners[*li.spec.ID] = li

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("WriteCheckpoint", mock.Anything, mock.MatchedBy(func(cp *apitypes.EventStreamCheckpoint) bool {
		return cp.StreamID.Equals(es.spec.ID) && cp.Listeners[*li.spec.ID] == nil
	})).Return(nil)

	es.checkpointInterval = 1 * time.Microsecond

	es.batchLoop(ss)

	msp.AssertExpectations(t)
	mcm.AssertExpectations(t)
}

func TestHWMCheckpointFail(t *testing.T) {

	es := newTestEventStream(t, `{
		"name": "ut_stream"
	}`)

	ss := &startedStreamState{
		updates:       make(chan *ffcapi.ListenerEvent, 1),
		batchLoopDone: make(chan struct{}),
	}
	ss.ctx, ss.cancelCtx = context.WithCancel(context.Background())

	li := &listener{
		spec: &apitypes.Listener{ID: fftypes.NewUUID()},
	}

	mcm := &confirmationsmocks.Manager{}
	mcm.On("CheckInFlight", li.spec.ID).Return(false)
	es.confirmations = mcm
	es.listeners[*li.spec.ID] = li

	mfc := es.connector.(*ffcapimocks.API)
	mfc.On("EventListenerHWM", mock.Anything, mock.MatchedBy(func(req *ffcapi.EventListenerHWMRequest) bool {
		return req.StreamID.Equals(es.spec.ID) && req.ListenerID.Equals(li.spec.ID)
	})).Run(func(args mock.Arguments) {
		ss.cancelCtx()
	}).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("pop"))

	msp := es.persistence.(*persistencemocks.Persistence)
	msp.On("WriteCheckpoint", mock.Anything, mock.MatchedBy(func(cp *apitypes.EventStreamCheckpoint) bool {
		return cp.StreamID.Equals(es.spec.ID) && cp.Listeners[*li.spec.ID] == nil
	})).Return(nil)

	es.checkpointInterval = 1 * time.Microsecond

	es.batchLoop(ss)

	mfc.AssertExpectations(t)
	msp.AssertExpectations(t)
	mcm.AssertExpectations(t)
}
