package pubsub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// PubSub wraps a NATS connection with JetStream for durable pub/sub.
type PubSub struct {
	conn *nats.Conn
	js   nats.JetStreamContext
}

type transientSubscription struct {
	sub *nats.Subscription
}

func (s transientSubscription) Close() error {
	if s.sub == nil {
		return nil
	}
	return s.sub.Unsubscribe()
}

// New connects to NATS, creates a JetStream context, and ensures the ALOQA
// stream exists covering all "aloqa.>" subjects.
func New(url string) (*PubSub, error) {
	conn, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(10),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("nats disconnected", slog.Any("error", err))
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.Info("nats reconnected", slog.String("url", c.ConnectedUrl()))
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to nats: %w", err)
	}

	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("creating jetstream context: %w", err)
	}

	// Create or update the ALOQA stream.
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      "ALOQA",
		Subjects:  []string{"aloqa.>"},
		Retention: nats.InterestPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   nats.FileStorage,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("creating ALOQA stream: %w", err)
	}

	slog.Info("nats connected", slog.String("url", conn.ConnectedUrl()))

	return &PubSub{conn: conn, js: js}, nil
}

// Publish sends data to the given subject through JetStream.
func (ps *PubSub) Publish(ctx context.Context, subject string, data []byte) error {
	return ps.PublishWithID(ctx, subject, data, "")
}

func (ps *PubSub) PublishWithID(ctx context.Context, subject string, data []byte, msgID string) error {
	opts := []nats.PubOpt{nats.Context(ctx)}
	if msgID != "" {
		opts = append(opts, nats.MsgId(msgID))
	}
	_, err := ps.js.Publish(subject, data, opts...)
	if err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}
	return nil
}

// Subscribe creates a push subscription on the given subject. The handler
// receives the raw message bytes and the subject.
func (ps *PubSub) Subscribe(subject string, handler func(data []byte, subject string)) (*nats.Subscription, error) {
	sub, err := ps.js.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data, msg.Subject)
		if err := msg.Ack(); err != nil {
			slog.Warn("failed to ack nats message", "subject", msg.Subject, "error", err)
		}
	}, nats.DeliverNew())
	if err != nil {
		return nil, fmt.Errorf("subscribing to %s: %w", subject, err)
	}
	return sub, nil
}

// PublishTransient sends data over core NATS without JetStream durability.
// This is intended for low-latency cluster coordination and media fanout.
func (ps *PubSub) PublishTransient(ctx context.Context, subject string, data []byte, headers map[string]string) error {
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
	}
	if len(headers) > 0 {
		msg.Header = nats.Header{}
		for key, value := range headers {
			msg.Header.Set(key, value)
		}
	}
	if err := ps.conn.PublishMsg(msg); err != nil {
		return fmt.Errorf("publishing transient to %s: %w", subject, err)
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
}

// SubscribeTransient creates a core NATS subscription for low-latency events.
func (ps *PubSub) SubscribeTransient(subject string, handler func(data []byte, subject string, headers map[string]string)) (io.Closer, error) {
	sub, err := ps.conn.Subscribe(subject, func(msg *nats.Msg) {
		headers := make(map[string]string, len(msg.Header))
		for key, values := range msg.Header {
			if len(values) == 0 {
				continue
			}
			headers[key] = values[0]
		}
		handler(msg.Data, msg.Subject, headers)
	})
	if err != nil {
		return nil, fmt.Errorf("subscribing transient to %s: %w", subject, err)
	}
	return transientSubscription{sub: sub}, nil
}

// Close drains the NATS connection and shuts it down gracefully.
func (ps *PubSub) Close() {
	if err := ps.conn.Drain(); err != nil {
		slog.Error("draining nats connection", slog.Any("error", err))
	}
}
