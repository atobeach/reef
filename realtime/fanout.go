package realtime

import "context"

// Fanout carries published messages to every server instance, each
// delivering to its own connections. A single instance needs none (nil:
// in-process delivery); across several, MaxConnections stays per instance.
type Fanout interface {
	// Publish hands m to every instance, this one included.
	Publish(ctx context.Context, m Message) error
	// Subscribe calls deliver for each message published by any instance
	// until ctx ends.
	Subscribe(ctx context.Context, deliver func(Message)) error
}
