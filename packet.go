// Copyright 2012 Google, Inc. All rights reserved.

package gopacket

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

// CaptureInfo contains capture metadata for a packet.  If a packet was captured
// off the wire or read from a pcap file (see the 'pcap' subdirectory), this
// information will be attached to the packet.
type CaptureInfo struct {
	// Populated is set to true if the rest of the CaptureInfo has been populated
	// with actual information.  If Populated is false, there's no point in
	// reading any of the other fields.
	Populated             bool
	Timestamp             time.Time
	CaptureLength, Length int
}

// Packet is the primary object used by gopacket.  Packets are created by a
// Decoder's Decode call.  A packet is made up of a set of Data, which
// is broken into a number of Layers as it is decoded.
type Packet interface {
	fmt.Stringer
	// Data returns all data associated with this packet
	Data() []byte
	// Layers returns all layers in this packet, computing them as necessary
	Layers() []Layer
	// Layer returns the first layer in this packet of the given type, or nil
	Layer(LayerType) Layer
	// LayerClass returns the first layer in this packet of the given class,
	// or nil.
	LayerClass(LayerClass) Layer
	// CaptureInfo returns the caputure information for this packet.  This returns
	// a pointer to the packet's struct, so it can be used both for reading and
	// writing the information.
	CaptureInfo() *CaptureInfo

	// LinkLayer returns the first link layer in the packet
	LinkLayer() LinkLayer
	// NetworkLayer returns the first network layer in the packet
	NetworkLayer() NetworkLayer
	// TransportLayer returns the first transport layer in the packet
	TransportLayer() TransportLayer
	// ApplicationLayer returns the first application layer in the packet
	ApplicationLayer() ApplicationLayer
	// ErrorLayer is particularly useful, since it returns nil if the packet
	// was fully decoded successfully, and non-nil if an error was encountered
	// in decoding and the packet was only partially decoded.  Thus, its output
	// can be used to determine if the entire packet was able to be decoded.
	ErrorLayer() ErrorLayer
}

// packet contains all the information we need to fulfill the Packet interface,
// and its two "subclasses" (yes, no such thing in Go, bear with me),
// eagerPacket and lazyPacket, provide eager and lazy decoding logic around the
// various functions needed to access this information.
type packet struct {
	// data contains the entire packet data for a packet
	data []byte
	// initialLayers is space for an initial set of layers already created inside
	// the packet.
	initialLayers [6]Layer
	// layers contains each layer we've already decoded
	layers []Layer
	// last is the last layer added to the packet
	last Layer
	// capInfo is the CaptureInfo for this packet
	capInfo CaptureInfo

	// Pointers to the various important layers
	link        LinkLayer
	network     NetworkLayer
	transport   TransportLayer
	application ApplicationLayer
	failure     ErrorLayer
}

func (p *packet) SetLinkLayer(l LinkLayer) {
	if p.link == nil {
		p.link = l
	}
}
func (p *packet) SetNetworkLayer(l NetworkLayer) {
	if p.network == nil {
		p.network = l
	}
}
func (p *packet) SetTransportLayer(l TransportLayer) {
	if p.transport == nil {
		p.transport = l
	}
}
func (p *packet) SetApplicationLayer(l ApplicationLayer) {
	if p.application == nil {
		p.application = l
	}
}
func (p *packet) SetErrorLayer(l ErrorLayer) {
	if p.failure == nil {
		p.failure = l
	}
}
func (p *packet) AddLayer(l Layer) {
	p.layers = append(p.layers, l)
	p.last = l
}
func (p *packet) CaptureInfo() *CaptureInfo {
	return &p.capInfo
}
func (p *packet) Data() []byte {
	return p.data
}
func (p *packet) recoverDecodeError() {
	if r := recover(); r != nil {
		fail := &DecodeFailure{err: fmt.Errorf("%v", r)}
		if p.last == nil {
			fail.data = p.data
		} else {
			fail.data = p.last.LayerPayload()
		}
		p.AddLayer(fail)
	}
}
func packetString(pLayers []Layer) string {
	var b bytes.Buffer
	for i, l := range pLayers {
		fmt.Fprintf(&b, "--- Layer %d: %v ---\n", i+1, l.LayerType())
		b.WriteString(l.String())
		b.WriteString("\n")
	}
	return b.String()
}

// eagerPacket is a packet implementation that does eager decoding.  Upon
// initial construction, it decodes all the layers it can from packet data.
// eagerPacket implements Packet and PacketBuilder.
type eagerPacket struct {
	packet
}

var nilDecoderError = errors.New("NextDecoder passed nil decoder, probably an unsupported decode type")

func (p *eagerPacket) NextDecoder(next Decoder) error {
	if next == nil {
		return nilDecoderError
	}
	if p.last == nil {
		return errors.New("NextDecoder called, but no layers added yet")
	}
	d := p.last.LayerPayload()
	if len(d) == 0 {
		return nil
	}
	// Since we're eager, immediately call the next decoder.
	return next.Decode(d, p)
}
func (p *eagerPacket) initialDecode(dec Decoder) {
	defer p.recoverDecodeError()
	err := dec.Decode(p.data, p)
	if err != nil {
		panic(err)
	}
}
func (p *eagerPacket) LinkLayer() LinkLayer {
	return p.link
}
func (p *eagerPacket) NetworkLayer() NetworkLayer {
	return p.network
}
func (p *eagerPacket) TransportLayer() TransportLayer {
	return p.transport
}
func (p *eagerPacket) ApplicationLayer() ApplicationLayer {
	return p.application
}
func (p *eagerPacket) ErrorLayer() ErrorLayer {
	return p.failure
}
func (p *eagerPacket) Layers() []Layer {
	return p.layers
}
func (p *eagerPacket) Layer(t LayerType) Layer {
	for _, l := range p.layers {
		if l.LayerType() == t {
			return l
		}
	}
	return nil
}
func (p *eagerPacket) LayerClass(lc LayerClass) Layer {
	for _, l := range p.layers {
		if lc.Contains(l.LayerType()) {
			return l
		}
	}
	return nil
}
func (p *eagerPacket) String() string { return packetString(p.Layers()) }

// lazyPacket does lazy decoding on its packet data.  On construction it does
// no initial decoding.  For each function call, it decodes only as many layers
// as are necessary to compute the return value for that function.
// lazyPacket implements Packet and PacketBuilder.
type lazyPacket struct {
	packet
	next Decoder
}

func (p *lazyPacket) NextDecoder(next Decoder) error {
	if next == nil {
		return nilDecoderError
	}
	p.next = next
	return nil
}
func (p *lazyPacket) decodeNextLayer() {
	if p.next == nil {
		return
	}
	d := p.data
	if p.last != nil {
		d = p.last.LayerPayload()
	}
	next := p.next
	p.next = nil
	// We've just set p.next to nil, so if we see we have no data, this should be
	// the final call we get to decodeNextLayer if we return here.
	if len(d) == 0 {
		return
	}
	defer p.recoverDecodeError()
	err := next.Decode(d, p)
	if err != nil {
		panic(err)
	}
}
func (p *lazyPacket) LinkLayer() LinkLayer {
	for p.link == nil && p.next != nil {
		p.decodeNextLayer()
	}
	return p.link
}
func (p *lazyPacket) NetworkLayer() NetworkLayer {
	for p.network == nil && p.next != nil {
		p.decodeNextLayer()
	}
	return p.network
}
func (p *lazyPacket) TransportLayer() TransportLayer {
	for p.transport == nil && p.next != nil {
		p.decodeNextLayer()
	}
	return p.transport
}
func (p *lazyPacket) ApplicationLayer() ApplicationLayer {
	for p.application == nil && p.next != nil {
		p.decodeNextLayer()
	}
	return p.application
}
func (p *lazyPacket) ErrorLayer() ErrorLayer {
	for p.failure == nil && p.next != nil {
		p.decodeNextLayer()
	}
	return p.failure
}
func (p *lazyPacket) Layers() []Layer {
	for p.next != nil {
		p.decodeNextLayer()
	}
	return p.layers
}
func (p *lazyPacket) Layer(t LayerType) Layer {
	for _, l := range p.layers {
		if l.LayerType() == t {
			return l
		}
	}
	numLayers := len(p.layers)
	for p.next != nil {
		p.decodeNextLayer()
		for _, l := range p.layers[numLayers:] {
			if l.LayerType() == t {
				return l
			}
		}
		numLayers = len(p.layers)
	}
	return nil
}
func (p *lazyPacket) LayerClass(lc LayerClass) Layer {
	for _, l := range p.layers {
		if lc.Contains(l.LayerType()) {
			return l
		}
	}
	numLayers := len(p.layers)
	for p.next != nil {
		p.decodeNextLayer()
		for _, l := range p.layers[numLayers:] {
			if lc.Contains(l.LayerType()) {
				return l
			}
		}
		numLayers = len(p.layers)
	}
	return nil
}
func (p *lazyPacket) String() string { return packetString(p.Layers()) }

// DecodeOptions tells gopacket how to decode a packet.
type DecodeOptions struct {
	// Lazy decoding decodes the minimum number of layers needed to return data
	// for a packet at each function call.  Be careful using this with concurrent
	// packet processors, as each call to packet.* could mutate the packet, and
	// two concurrent function calls could interact poorly.
	Lazy bool
	// NoCopy decoding doesn't copy its input buffer into storage that's owned by
	// the packet.  If you can guarantee that the bytes underlying the slice
	// passed into NewPacket aren't going to be modified, this can be faster.  If
	// there's any chance that those bytes WILL be changed, this will invalidate
	// your packets.
	NoCopy bool
}

// Default decoding provides the safest (but slowest) method for decoding
// packets.  It eagerly processes all layers (so it's concurrency-safe) and it
// copies its input buffer upon creation of the packet (so the packet remains
// valid if the underlying slice is modified.  Both of these take time,
// though, so beware.  If you can guarantee that the packet will only be used
// by one goroutine at a time, set Lazy decoding.  If you can guarantee that
// the underlying slice won't change, set NoCopy decoding.
var Default DecodeOptions = DecodeOptions{}

// Lazy is a DecodeOptions with just Lazy set.
var Lazy DecodeOptions = DecodeOptions{Lazy: true}

// NoCopy is a DecodeOptions with just NoCopy set.
var NoCopy DecodeOptions = DecodeOptions{NoCopy: true}

// NewPacket creates a new Packet object from a set of bytes.  The
// firstLayerDecoder tells it how to interpret the first layer from the bytes,
// future layers will be generated from that first layer automatically.
func NewPacket(data []byte, firstLayerDecoder Decoder, options DecodeOptions) Packet {
	if !options.NoCopy {
		dataCopy := make([]byte, len(data))
		copy(dataCopy, data)
		data = dataCopy
	}
	if options.Lazy {
		p := &lazyPacket{
			packet: packet{data: data},
			next:   firstLayerDecoder,
		}
		p.layers = p.initialLayers[:0]
		// Crazy craziness:
		// If the following return statemet is REMOVED, and Lazy is FALSE, then
		// eager packet processing becomes 17% FASTER.  No, there is no logical
		// explanation for this.  However, it's such a hacky micro-optimization that
		// we really can't rely on it.  It appears to have to do with the size the
		// compiler guesses for this function's stack space, since one symptom is
		// that with the return statement in place, we more than double calls to
		// runtime.morestack/runtime.lessstack.  We'll hope the compiler gets better
		// over time and we get this optimization for free.  Until then, we'll have
		// to live with slower packet processing.
		return p
	}
	p := &eagerPacket{
		packet: packet{data: data},
	}
	p.layers = p.initialLayers[:0]
	p.initialDecode(firstLayerDecoder)
	return p
}

// PacketDataSource is an interface for some source of packet data.  Users may
// create their own implementations, or use the existing implementations in
// gopacket/pcap (libpcap, allows reading from live interfaces or from 
// pcap files) or gopacket/pfring (PF_RING, allows reading from live
// interfaces).
type PacketDataSource interface {
	// ReadPacketData returns the next packet available from this data source.
	// It returns:
	//  data:  The bytes of an individual packet.
	//  ci:  Metadata about the capture
	//  err:  An error encountered while reading packet data.  If err != nil,
	//    then data/ci will be ignored.
	ReadPacketData() (data []byte, ci CaptureInfo, err error)
}

// PacketSource reads in packets from a PacketDataSource, decodes them, and
// returns them.
//
// There are currently two different methods for reading packets in through
// a PacketSource:
//
// Reading With Packets Function
//
// This method is the most convenient and easiest to code, but lacks
// flexibility.  Packets returns a 'chan Packet', then asynchronously writes
// packets into that channel.  Packets uses a blocking channel, and closes
// it if an io.EOF is returned by the underlying PacketDataSource.  All other
// PacketDataSource errors are ignored and discarded.
//  for packet := range packetSource.Packets() {
//    ...
//  }
//
// Reading With NextPacket Function
//
// This method is the most flexible, and exposes errors that may be
// encountered by the underlying PacketDataSource.  It's also the fastest
// in a tight loop, since it doesn't have the overhead of a channel
// read/write.  However, it requires the user to handle errors, most
// importantly the io.EOF error in cases where packets are being read from
// a file.
//  for {
//    packet, err := packetSource.NextPacket() {
//    if err == io.EOF {
//      break
//    } else if err != nil {
//      log.Println("Error:", err)
//      continue
//    }
//    handlePacket(packet)  // Do something with each packet.
//  }
type PacketSource struct {
	source  PacketDataSource
	decoder Decoder
	// DecodeOptions is the set of options to use for decoding each piece
	// of packet data.  This can/should be changed by the user to reflect the
	// way packets should be decoded.
	DecodeOptions
}

// NewPacketSource creates a packet data source.  
func NewPacketSource(source PacketDataSource, decoder Decoder) *PacketSource {
	return &PacketSource{
		source:  source,
		decoder: decoder,
	}
}

// NextPacket returns the next decoded packet from the PacketSource.  On error,
// it returns a nil packet and a non-nil error.
func (p *PacketSource) NextPacket() (Packet, error) {
	data, ci, err := p.source.ReadPacketData()
	if err != nil {
		return nil, err
	}
	packet := NewPacket(data, p.decoder, p.DecodeOptions)
	*packet.CaptureInfo() = ci
	return packet, nil
}

// packetsToChannel reads in all packets from the packet source and sends them
// to the given channel.  When it receives an error, it ignores it.  When it
// receives an io.EOF, it closes the channel.
func (p *PacketSource) packetsToChannel(c chan<- Packet) {
	for {
		packet, err := p.NextPacket()
		if err == io.EOF {
			close(c)
			return
		} else if err == nil {
			c <- packet
		}
	}
}

// Packets returns a blocking channel of packets, allowing easy iterating over
// packets.  Packets will be asynchronously read in from the underlying
// PacketDataSource and written to the returned channel.  If the underlying
// PacketDataSource returns an io.EOF error, the channel will be closed.
// If any other error is encountered, it is ignored.
//
//  for packet := range packetSource.Packets() {
//    handlePacket(packet)  // Do something with each packet.
//  }
func (p *PacketSource) Packets() chan Packet {
	c := make(chan Packet)
	go p.packetsToChannel(c)
	return c
}