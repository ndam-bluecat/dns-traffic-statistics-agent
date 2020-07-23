// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Package dns provides support for parsing DNS messages and reporting the
// results. This package supports the DNS protocol as defined by RFC 1034
// and RFC 1035. It does not have any special support for RFC 2671 (EDNS) or
// RFC 4035 (DNS Security Extensions), but since those specifications only
// add backwards compatible features there will be no issues handling the
// messages.
package dns

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"reflect"

	"github.com/Shopify/sarama"

	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/monitoring"

	"github.com/elastic/beats/packetbeat/model"
	"github.com/elastic/beats/packetbeat/protos"

	"github.com/elastic/beats/packetbeat/statsdns"
	"github.com/elastic/beats/packetbeat/utils"
	mkdns "github.com/miekg/dns"
	"golang.org/x/net/publicsuffix"
)

type dnsPlugin struct {
	// Configuration data.
	ports              []int
	sendRequest        bool
	sendResponse       bool
	includeAuthorities bool
	includeAdditionals bool

	// Cache of active DNS transactions. The map key is the HashableDnsTuple
	// associated with the request.
	transactions       *common.Cache
	transactionTimeout time.Duration

	results protos.Reporter // Channel where results are pushed.

	// [Bluecat]
	dropDecodedPacket bool

}

var (
	debugf = logp.MakeDebug("dns")
)

const maxDNSTupleRawSize = 16 + 16 + 2 + 2 + 4 + 1

// Constants used to associate the DNS QR flag with a meaningful value.
const (
	query    = false
	response = true
)

// Transport protocol.
type transport uint8

var (
	unmatchedRequests  = monitoring.NewInt(nil, "dns.unmatched_requests")
	unmatchedResponses = monitoring.NewInt(nil, "dns.unmatched_responses")
	// listTruncateRespID = make(map[uint16]bool)
)

const (
	transportTCP = iota
	transportUDP
)

var transportNames = []string{
	"tcp",
	"udp",
}

func (t transport) String() string {
	if int(t) >= len(transportNames) {
		return "impossible"
	}
	return transportNames[t]
}

type hashableDNSTuple [maxDNSTupleRawSize]byte

// DnsMessage contains a single DNS message.
type dnsMessage struct {
	ts           time.Time          // Time when the message was received.
	tuple        common.IPPortTuple // Source and destination addresses of packet.
	cmdlineTuple *common.CmdlineTuple
	data         *mkdns.Msg // Parsed DNS packet data.
	length       int        // Length of the DNS message in bytes (without DecodeOffset).
}

// DnsTuple contains source IP/port, destination IP/port, transport protocol,
// and DNS ID.
type dnsTuple struct {
	common.BaseTuple
	ipLength  int
	transport transport
	id        uint16

	raw    hashableDNSTuple // Src_ip:Src_port:Dst_ip:Dst_port:Transport:Id
	revRaw hashableDNSTuple // Dst_ip:Dst_port:Src_ip:Src_port:Transport:Id
}

func dnsTupleFromIPPort(t *common.IPPortTuple, trans transport, id uint16) dnsTuple {
	tuple := dnsTuple{
		ipLength: t.IPLength,
		BaseTuple: common.BaseTuple{
			SrcIP:   t.SrcIP,
			DstIP:   t.DstIP,
			SrcPort: t.SrcPort,
			DstPort: t.DstPort,
		},
		transport: trans,
		id:        id,
	}
	tuple.computeHashables()

	return tuple
}

func (t dnsTuple) reverse() dnsTuple {
	return dnsTuple{
		ipLength: t.ipLength,
		BaseTuple: common.BaseTuple{
			SrcIP:   t.DstIP,
			DstIP:   t.SrcIP,
			SrcPort: t.DstPort,
			DstPort: t.SrcPort,
		},
		transport: t.transport,
		id:        t.id,
		raw:       t.revRaw,
		revRaw:    t.raw,
	}
}

func (t *dnsTuple) computeHashables() {
	copy(t.raw[0:16], t.SrcIP)
	copy(t.raw[16:18], []byte{byte(t.SrcPort >> 8), byte(t.SrcPort)})
	copy(t.raw[18:34], t.DstIP)
	copy(t.raw[34:36], []byte{byte(t.DstPort >> 8), byte(t.DstPort)})
	copy(t.raw[36:38], []byte{byte(t.id >> 8), byte(t.id)})
	t.raw[39] = byte(t.transport)

	copy(t.revRaw[0:16], t.DstIP)
	copy(t.revRaw[16:18], []byte{byte(t.DstPort >> 8), byte(t.DstPort)})
	copy(t.revRaw[18:34], t.SrcIP)
	copy(t.revRaw[34:36], []byte{byte(t.SrcPort >> 8), byte(t.SrcPort)})
	copy(t.revRaw[36:38], []byte{byte(t.id >> 8), byte(t.id)})
	t.revRaw[39] = byte(t.transport)
}

func (t *dnsTuple) String() string {
	return fmt.Sprintf("DnsTuple src[%s:%d] dst[%s:%d] transport[%s] id[%d]",
		t.SrcIP.String(),
		t.SrcPort,
		t.DstIP.String(),
		t.DstPort,
		t.transport,
		t.id)
}

// Hashable returns a hashable value that uniquely identifies
// the DNS tuple.
func (t *dnsTuple) hashable() hashableDNSTuple {
	return t.raw
}

// Hashable returns a hashable value that uniquely identifies
// the DNS tuple after swapping the source and destination.
func (t *dnsTuple) revHashable() hashableDNSTuple {
	return t.revRaw
}

// getTransaction returns the transaction associated with the given
// HashableDnsTuple. The lookup key should be the HashableDnsTuple associated
// with the request (src is the requestor). Nil is returned if the entry
// does not exist.
func (dns *dnsPlugin) getTransaction(k hashableDNSTuple) *dnsTransaction {
	v := dns.transactions.Get(k)
	if v != nil {
		return v.(*dnsTransaction)
	}
	return nil
}

type dnsTransaction struct {
	ts           time.Time // Time when the request was received.
	tuple        dnsTuple  // Key used to track this transaction in the transactionsMap.
	responseTime int32     // Elapsed time in milliseconds between the request and response.
	src          common.Endpoint
	dst          common.Endpoint
	transport    transport
	notes        []string

	request  *dnsMessage
	response *dnsMessage
}

func init() {
	protos.Register("dns", New)
}

var compressionModes = map[string]sarama.CompressionCodec{
	"none":   sarama.CompressionNone,
	"no":     sarama.CompressionNone,
	"off":    sarama.CompressionNone,
	"gzip":   sarama.CompressionGZIP,
	"lz4":    sarama.CompressionLZ4,
	"snappy": sarama.CompressionSnappy,
}

func New(
	testMode bool,
	results protos.Reporter,
	cfg *common.Config,
) (protos.Plugin, error) {
	p := &dnsPlugin{}
	dnsCfg := defaultConfig
	if !testMode {
		if err := cfg.Unpack(&dnsCfg); err != nil {
			return nil, err
		}
	}

	if err := p.init(results, &dnsCfg); err != nil {
		return nil, err
	}

	return p, nil
}

func (dns *dnsPlugin) init(results protos.Reporter, config *dnsConfig) error {
	dns.setFromConfig(config)
	dns.transactions = common.NewCacheWithRemovalListener(
		dns.transactionTimeout,
		protos.DefaultTransactionHashSize,
		func(k common.Key, v common.Value) {
			trans, ok := v.(*dnsTransaction)
			if !ok {
				logp.Err("Expired value is not a *DnsTransaction.")
				return
			}
			dns.expireTransaction(trans)
		})
	dns.transactions.StartJanitor(dns.transactionTimeout)

	dns.results = results

	// [Bluecat]
	dns.dropDecodedPacket = config.DropDecodedPacket

	return nil
}

func (dns *dnsPlugin) setFromConfig(config *dnsConfig) error {
	dns.ports = config.Ports
	dns.sendRequest = config.SendRequest
	dns.sendResponse = config.SendResponse
	dns.includeAuthorities = config.IncludeAuthorities
	dns.includeAdditionals = config.IncludeAdditionals
	dns.transactionTimeout = config.TransactionTimeout
	return nil
}

func newTransaction(ts time.Time, tuple dnsTuple, cmd common.CmdlineTuple) *dnsTransaction {
	trans := &dnsTransaction{
		transport: tuple.transport,
		ts:        ts,
		tuple:     tuple,
	}
	trans.src, trans.dst = common.MakeEndpointPair(tuple.BaseTuple, &cmd)
	return trans
}

// deleteTransaction deletes an entry from the transaction map and returns
// the deleted element. If the key does not exist then nil is returned.
func (dns *dnsPlugin) deleteTransaction(k hashableDNSTuple) *dnsTransaction {
	v := dns.transactions.Delete(k)
	if v != nil {
		return v.(*dnsTransaction)
	}
	return nil
}

func (dns *dnsPlugin) GetPorts() []int {
	return dns.ports
}

func (dns *dnsPlugin) ConnectionTimeout() time.Duration {
	return dns.transactionTimeout
}

func (dns *dnsPlugin) receivedDNSRequest(tuple *dnsTuple, msg *dnsMessage) {
	// [Bluecat]
	debugf("DNS Processing query. %s", tuple.String())
	srcIP := msg.tuple.SrcIP.String()
	dstIP := msg.tuple.DstIP.String()
	isDuplicated := false

	// Don't receive internal DNS request
	if utils.IsInternalCall(srcIP, dstIP) {
		return
	}

	trans := dns.deleteTransaction(tuple.hashable())
	if trans != nil {
		// This happens if a client puts multiple requests in flight
		// with the same ID.

		trans.notes = append(trans.notes, duplicateQueryMsg.Error())
		debugf("%s %s", duplicateQueryMsg.Error(), tuple.String())
		// More log to debug duplicate
		debugf("Duplicate - Old Request: reqID=%d - time=%s - question=%v", trans.request.data.MsgHdr.Id, trans.request.ts, trans.request.data.Question)
		debugf("Duplicate - New Request: reqID=%d - time=%s - question=%v", msg.data.MsgHdr.Id, msg.ts, msg.data.Question)

		// This case: filter duplicate msg has same time
		// Avoid af_packet issue that capture the same packet twice
		if reflect.DeepEqual(trans.request.ts, msg.ts) {
			dns.transactions.Put(tuple.hashable(), trans)
			return
		}

		if reflect.DeepEqual(trans.request.data.Question, msg.data.Question){
			//Bluecat Check Duplicate Messsage
			isDuplicated = true
		}
		dns.publishTransaction(trans, false)
		dns.deleteTransaction(trans.tuple.hashable())
	}

	logp.Info("Request infor: srcIP=%v - dstIP=%v reqID=%d - time=%s - question=%v", srcIP, dstIP, msg.data.MsgHdr.Id, msg.ts, msg.data.Question)
	//Bluecat
	queryDNS := statsdns.NewQueryDNS(srcIP, dstIP, isDuplicated)
	statsdns.QStatDNS.PushQueryDNS(queryDNS)

	trans = newTransaction(msg.ts, *tuple, *msg.cmdlineTuple)

	if tuple.transport == transportUDP && (msg.data.IsEdns0() != nil) && msg.length > maxDNSPacketSize {
		trans.notes = append(trans.notes, udpPacketTooLarge.Error())
		debugf("%s", udpPacketTooLarge.Error())
	}

	dns.transactions.Put(tuple.hashable(), trans)
	trans.request = msg
	// Bluecat Store all request messages for the recursion counting purpose
	if trans.request != nil && trans.request.data != nil {
		statsdns.AddRequestMsgMap(trans.src.IP, trans.dst.IP, tuple.id, trans.request.data.Question)
	}
}

func (dns *dnsPlugin) receivedDNSResponse(tuple *dnsTuple, msg *dnsMessage) {
	// [Bluecat]
	debugf("Processing response. %s", tuple.String())
	srcIP := msg.tuple.SrcIP.String()
	dstIP := msg.tuple.DstIP.String()
	isDrop := false
	// Don't receive internal DNS response
	if utils.IsInternalCall(srcIP, dstIP) {
		return
	}

	trans := dns.getTransaction(tuple.revHashable())
	if trans == nil {
		trans = newTransaction(msg.ts, tuple.reverse(), msg.cmdlineTuple.Reverse())
		trans.notes = append(trans.notes, orphanedResponse.Error())
		// [Bluecat]
		debugf("%s %s", orphanedResponse.Error(), tuple.String())
		isDrop = true
		unmatchedResponses.Add(1)
	}

	trans.response = msg

	if tuple.transport == transportUDP {
		respIsEdns := msg.data.IsEdns0() != nil
		if !respIsEdns && msg.length > maxDNSPacketSize {
			trans.notes = append(trans.notes, udpPacketTooLarge.responseError())
			debugf("%s", udpPacketTooLarge.responseError())
		}

		request := trans.request
		if request != nil {
			reqIsEdns := request.data.IsEdns0() != nil

			switch {
			case reqIsEdns && !respIsEdns:
				trans.notes = append(trans.notes, respEdnsNoSupport.Error())
				debugf("%s %s", respEdnsNoSupport.Error(), tuple.String())
			case !reqIsEdns && respIsEdns:
				trans.notes = append(trans.notes, respEdnsUnexpected.Error())
				debugf("%s %s", respEdnsUnexpected.Error(), tuple.String())
			}
		}
	}
	// Bluecat Determine the recursion query
	if trans.request != nil && trans.request.data != nil {
		statsdns.CalculateRecursiveMsg(trans.src.IP, trans.dst.IP, tuple.id, trans.request.data.Question, trans.response.data)
	}

	dns.publishTransaction(trans, isDrop)
	dns.deleteTransaction(trans.tuple.hashable())
}

func (dns *dnsPlugin) publishTransaction(t *dnsTransaction, isDrop bool) {
	debugf("publishTransaction")
	if dns.results == nil {
		return
	}

	// [Bluecat]
	if dns.dropDecodedPacket {
		// Do nothing, just drop the decoded messages
		// Use this for testing/benchmarking only
		return
	}

	//[Bluecat]
	record := &model.Record{DNS: &model.DNS{}}
	timestamp := t.ts
	record.Type = "dns"
	record.Timestamp = timestamp.Format(time.RFC3339)
	record.Transport = t.transport.String()
	record.Src = &t.src
	record.Dst = &t.dst
	record.Status = common.ERROR_STATUS

	if len(t.notes) == 1 {
		record.Notes = t.notes[0]
	} else if len(t.notes) > 1 {
		record.Notes = strings.Join(t.notes, " ")
	}

	dnsRec := record.DNS

	if t.request != nil && t.response != nil {
		record.BytesIn = t.request.length
		record.BytesOut = t.response.length
		record.ResponseTime = float64(t.response.ts.Sub(t.ts).Nanoseconds()) / 1e6
		record.Method = dnsOpCodeToString(t.request.data.Opcode)
		if len(t.request.data.Question) > 0 {
			record.Query = dnsQuestionToString(t.request.data.Question[0])
			record.Resource = t.request.data.Question[0].Name
		}
		dnsRec = toDNSRecord(t.response.data, dns.includeAuthorities, dns.includeAdditionals)
		if t.response.data.Rcode == 0 {
			record.Status = common.OK_STATUS
		}
	} else if t.request != nil {
		record.BytesIn = t.request.length
		record.Method = dnsOpCodeToString(t.request.data.Opcode)
		if len(t.request.data.Question) > 0 {
			record.Query = dnsQuestionToString(t.request.data.Question[0])
			record.Resource = t.request.data.Question[0].Name
		}
		dnsRec = toDNSRecord(t.request.data, dns.includeAuthorities, dns.includeAdditionals)
		// [Bluecat]
		// if listTruncateRespID[t.request.data.Id] {
		// 	delete(listTruncateRespID, t.request.data.Id)
		// 	return
		// }
		// If transaction has not response, 
		// not need to increase TotalResponse and otherDNSType
		// [eg: Responses decode error]
		return
	} else if t.response != nil {
		record.BytesOut = t.response.length
		record.Method = dnsOpCodeToString(t.response.data.Opcode)
		if len(t.response.data.Question) > 0 {
			record.Query = dnsQuestionToString(t.response.data.Question[0])
			record.Resource = t.response.data.Question[0].Name
		}
		dnsRec = toDNSRecord(t.response.data, dns.includeAuthorities, dns.includeAdditionals)
	}
	//logp.debug("%v", dnsRec)

	record.DNS = dnsRec

	logp.Debug("Record Decoded", "%v", record)
	if !isDrop {
		statsdns.QStatDNS.PushRecordDNS(record)
	}
}

// dnsToString converts a DNS message to a string.
func dnsToString(dns *mkdns.Msg) string {
	var msgType string
	if dns.Response {
		msgType = "response"
	} else {
		msgType = "query"
	}

	var t []string
	if dns.Authoritative {
		t = append(t, "aa")
	}
	if dns.Truncated {
		t = append(t, "tc")
	}
	if dns.RecursionDesired {
		t = append(t, "rd")
	}
	if dns.RecursionAvailable {
		t = append(t, "ra")
	}
	if dns.AuthenticatedData {
		t = append(t, "ad")
	}
	if dns.CheckingDisabled {
		t = append(t, "cd")
	}
	flags := strings.Join(t, " ")

	var a []string
	a = append(a, fmt.Sprintf("ID %d; QR %s; OPCODE %s; FLAGS %s; RCODE %s",
		dns.Id, msgType, dnsOpCodeToString(dns.Opcode), flags,
		dnsResponseCodeToString(dns.Rcode)))

	if len(dns.Question) > 0 {
		t = []string{}
		for _, question := range dns.Question {
			t = append(t, dnsQuestionToString(question))
		}
		a = append(a, fmt.Sprintf("QUESTION %s", strings.Join(t, "; ")))
	}

	if len(dns.Answer) > 0 {
		a = append(a, fmt.Sprintf("ANSWER %s",
			rrsToString(dns.Answer)))
	}

	if len(dns.Ns) > 0 {
		a = append(a, fmt.Sprintf("AUTHORITY %s",
			rrsToString(dns.Ns)))
	}

	if len(dns.Extra) > 0 {
		a = append(a, fmt.Sprintf("ADDITIONAL %s",
			rrsToString(dns.Extra)))
	}

	return strings.Join(a, "; ")
}

func (dns *dnsPlugin) expireTransaction(t *dnsTransaction) {
	t.notes = append(t.notes, noResponse.Error())
	// debugf("%s %s", noResponse.Error(), t.tuple.String())
	dns.publishTransaction(t, true)
	unmatchedRequests.Add(1)
}

// Adds the DNS message data to the supplied MapStr.
func addDNSToMapStr(m common.MapStr, dns *mkdns.Msg, authority bool, additional bool) {
	m["id"] = dns.Id
	m["op_code"] = dnsOpCodeToString(dns.Opcode)

	m["flags"] = common.MapStr{
		"authoritative":       dns.Authoritative,
		"truncated_response":  dns.Truncated,
		"recursion_desired":   dns.RecursionDesired,
		"recursion_available": dns.RecursionAvailable,
		"authentic_data":      dns.AuthenticatedData, // [RFC4035]
		"checking_disabled":   dns.CheckingDisabled,  // [RFC4035]
	}
	m["response_code"] = dnsResponseCodeToString(dns.Rcode)

	if len(dns.Question) > 0 {
		q := dns.Question[0]
		qMapStr := common.MapStr{
			"name":  q.Name,
			"type":  dnsTypeToString(q.Qtype),
			"class": dnsClassToString(q.Qclass),
		}
		m["question"] = qMapStr

		eTLDPlusOne, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimRight(q.Name, "."))
		if err == nil {
			qMapStr["etld_plus_one"] = eTLDPlusOne + "."
		}
	}

	rrOPT := dns.IsEdns0()
	if rrOPT != nil {
		m["opt"] = optToMapStr(rrOPT)
	}

	m["answers_count"] = len(dns.Answer)
	if len(dns.Answer) > 0 {
		m["answers"] = rrsToMapStrs(dns.Answer)
	}

	m["authorities_count"] = len(dns.Ns)
	if authority && len(dns.Ns) > 0 {
		m["authorities"] = rrsToMapStrs(dns.Ns)
	}

	if rrOPT != nil {
		m["additionals_count"] = len(dns.Extra) - 1
	} else {
		m["additionals_count"] = len(dns.Extra)
	}
	if additional && len(dns.Extra) > 0 {
		rrsMapStrs := rrsToMapStrs(dns.Extra)
		// We do not want OPT RR to appear in the 'additional' section,
		// that's why rrsMapStrs could be empty even though len(dns.Extra) > 0
		if len(rrsMapStrs) > 0 {
			m["additionals"] = rrsMapStrs
		}
	}
}

func toDNSRecord(dns *mkdns.Msg, authority bool, additional bool) *model.DNS {
	r := &model.DNS{Flags: &model.Flags{}, Question: &model.Question{}, Opt: &model.Opt{}, Answers: make([]*model.Answer, 0), Authorities: make([]*model.Answer, 0), Additionals: make([]*model.Answer, 0)}

	r.ID = dns.Id
	r.OpCode = dnsOpCodeToString(dns.Opcode)

	r.Flags = &model.Flags{
		Authoritative:      dns.Authoritative,
		TruncatedResponse:  dns.Truncated,
		RecursionDesired:   dns.RecursionDesired,
		RecursionAvailable: dns.RecursionAvailable,
		AuthenticData:      dns.AuthenticatedData, // [RFC4035]
		CheckingDisabled:   dns.CheckingDisabled,  // [RFC4035]
	}
	r.ResponseCode = dnsResponseCodeToString(dns.Rcode)

	if len(dns.Question) > 0 {
		q := dns.Question[0]
		qMapStr := &model.Question{
			Name:  q.Name,
			Type:  dnsTypeToString(q.Qtype),
			Class: dnsClassToString(q.Qclass),
		}
		r.Question = qMapStr

		eTLDPlusOne, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimRight(q.Name, "."))
		if err == nil {
			qMapStr.EtldPlusOne = eTLDPlusOne + "."
		}
	}

	rrOPT := dns.IsEdns0()
	if rrOPT != nil {
		r.Opt = toOpt(rrOPT)
	}

	r.AnswersCount = len(dns.Answer)
	if len(dns.Answer) > 0 {
		r.Answers = rrsToDNSAnswers(dns.Answer)
	}

	r.AuthoritiesCount = len(dns.Ns)
	if authority && len(dns.Ns) > 0 {
		r.Authorities = rrsToDNSAnswers(dns.Ns)
	}

	if rrOPT != nil {
		r.AdditionalsCount = len(dns.Extra) - 1
	} else {
		r.AdditionalsCount = len(dns.Extra)
	}
	if additional && len(dns.Extra) > 0 {
		rrsMapStrs := rrsToDNSAnswers(dns.Extra)
		// // We do not want OPT RR to appear in the 'additional' section,
		// // that's why rrsMapStrs could be empty even though len(dns.Extra) > 0
		if len(rrsMapStrs) > 0 {
			r.Additionals = rrsMapStrs
		}
	}

	return r
}

func optToMapStr(rrOPT *mkdns.OPT) common.MapStr {
	optMapStr := common.MapStr{
		"do":        rrOPT.Do(), // true if DNSSEC
		"version":   strconv.FormatUint(uint64(rrOPT.Version()), 10),
		"udp_size":  rrOPT.UDPSize(),
		"ext_rcode": dnsResponseCodeToString(rrOPT.ExtendedRcode()),
	}
	for _, o := range rrOPT.Option {
		switch o.(type) {
		case *mkdns.EDNS0_DAU:
			optMapStr["dau"] = o.String()
		case *mkdns.EDNS0_DHU:
			optMapStr["dhu"] = o.String()
		case *mkdns.EDNS0_EXPIRE:
			optMapStr["local"] = o.String()
		case *mkdns.EDNS0_LLQ:
			optMapStr["llq"] = o.String()
		case *mkdns.EDNS0_LOCAL:
			optMapStr["local"] = o.String()
		case *mkdns.EDNS0_N3U:
			optMapStr["n3u"] = o.String()
		case *mkdns.EDNS0_NSID:
			optMapStr["nsid"] = o.String()
		case *mkdns.EDNS0_SUBNET:
			optMapStr["subnet"] = o.String()
		case *mkdns.EDNS0_COOKIE:
			optMapStr["cookie"] = o.String()
		case *mkdns.EDNS0_UL:
			optMapStr["ul"] = o.String()
		}
	}
	return optMapStr
}

func toOpt(rrOPT *mkdns.OPT) *model.Opt {
	opt := &model.Opt{
		Do:       rrOPT.Do(), // true if DNSSEC
		Version:  strconv.FormatUint(uint64(rrOPT.Version()), 10),
		UDPSize:  rrOPT.UDPSize(),
		ExtRcode: dnsResponseCodeToString(rrOPT.ExtendedRcode()),
	}
	for _, o := range rrOPT.Option {
		switch o.(type) {
		case *mkdns.EDNS0_DAU:
			opt.DAU = o.String()
		case *mkdns.EDNS0_DHU:
			opt.DHU = o.String()
		case *mkdns.EDNS0_EXPIRE:
			opt.LOCAL = o.String()
		case *mkdns.EDNS0_LLQ:
			opt.LLQ = o.String()
		case *mkdns.EDNS0_LOCAL:
			opt.LOCAL = o.String()
		case *mkdns.EDNS0_N3U:
			opt.N3U = o.String()
		case *mkdns.EDNS0_NSID:
			opt.NSID = o.String()
		case *mkdns.EDNS0_SUBNET:
			opt.SUBNET = o.String()
		case *mkdns.EDNS0_COOKIE:
			opt.COOKIE = o.String()
		case *mkdns.EDNS0_UL:
			opt.UL = o.String()
		}
	}
	return opt
}

// rrsToMapStr converts an slice of RR's to an slice of MapStr's.
func rrsToMapStrs(records []mkdns.RR) []common.MapStr {
	mapStrSlice := make([]common.MapStr, 0, len(records))
	for _, rr := range records {
		rrHeader := rr.Header()

		mapStr := rrToMapStr(rr)
		if len(mapStr) == 0 { // OPT pseudo-RR returns an empty MapStr
			continue
		}
		mapStr["name"] = rrHeader.Name
		mapStr["type"] = dnsTypeToString(rrHeader.Rrtype)
		mapStr["class"] = dnsClassToString(rrHeader.Class)
		mapStr["ttl"] = strconv.Itoa(int(rrHeader.Ttl))
		mapStrSlice = append(mapStrSlice, mapStr)
	}
	return mapStrSlice
}

func rrsToDNSAnswers(records []mkdns.RR) []*model.Answer {
	mapStrSlice := make([]*model.Answer, 0, len(records))
	for _, rr := range records {
		rrHeader := rr.Header()

		mapStr := rrToDNSAnswer(rr)
		if mapStr != nil {
			mapStr.Name = rrHeader.Name
			mapStr.Type = dnsTypeToString(rrHeader.Rrtype)
			mapStr.Class = dnsClassToString(rrHeader.Class)
			mapStr.TTL = strconv.Itoa(int(rrHeader.Ttl))
			mapStrSlice = append(mapStrSlice, mapStr)
		}
	}
	return mapStrSlice
}

// Convert all RDATA fields of a RR to a single string
// fields are ordered alphabetically with 'data' as the last element
//
// TODO An improvement would be to replace 'data' by the real field name
// It would require some changes in unit tests
func rrToString(rr mkdns.RR) string {
	var st string
	var keys []string

	mapStr := rrToMapStr(rr)
	data, ok := mapStr["data"]
	delete(mapStr, "data")

	for k := range mapStr {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b bytes.Buffer
	for _, k := range keys {
		v := mapStr[k]
		switch x := v.(type) {
		case int:
			fmt.Fprintf(&b, "%s %d, ", k, x)
		case string:
			fmt.Fprintf(&b, "%s %s, ", k, x)
		}
	}
	if !ok {
		st = strings.TrimSuffix(b.String(), ", ")
		return st
	}

	switch x := data.(type) {
	case int:
		fmt.Fprintf(&b, "%d", x)
	case string:
		fmt.Fprintf(&b, "%s", x)
	}
	return b.String()
}

func rrToMapStr(rr mkdns.RR) common.MapStr {
	mapStr := common.MapStr{}
	rrType := rr.Header().Rrtype

	switch x := rr.(type) {
	default:
		// We don't have special handling for this type
		debugf("No special handling for RR type %s", dnsTypeToString(rrType))
		unsupportedRR := new(mkdns.RFC3597)
		err := unsupportedRR.ToRFC3597(x)
		if err == nil {
			rData, err := hexStringToString(unsupportedRR.Rdata)
			mapStr["data"] = rData
			if err != nil {
				debugf("%s", err.Error())
			}
		} else {
			debugf("Rdata for the unhandled RR type %s could not be fetched", dnsTypeToString(rrType))
		}
	case *mkdns.A:
		mapStr["data"] = x.A.String()
	case *mkdns.AAAA:
		mapStr["data"] = x.AAAA.String()
	case *mkdns.CNAME:
		mapStr["data"] = x.Target
	case *mkdns.DNSKEY:
		mapStr["flags"] = strconv.Itoa(int(x.Flags))
		mapStr["protocol"] = strconv.Itoa(int(x.Protocol))
		mapStr["algorithm"] = dnsAlgorithmToString(x.Algorithm)
		mapStr["data"] = x.PublicKey
	case *mkdns.DS:
		mapStr["key_tag"] = strconv.Itoa(int(x.KeyTag))
		mapStr["algorithm"] = dnsAlgorithmToString(x.Algorithm)
		mapStr["digest_type"] = dnsHashToString(x.DigestType)
		mapStr["data"] = strings.ToUpper(x.Digest)
	case *mkdns.MX:
		mapStr["preference"] = x.Preference
		mapStr["data"] = x.Mx
	case *mkdns.NS:
		mapStr["data"] = x.Ns
	case *mkdns.NSEC:
		mapStr["type_bits"] = dnsTypeBitsMapToString(x.TypeBitMap)
		mapStr["data"] = x.NextDomain
	case *mkdns.NSEC3:
		mapStr["hash"] = dnsHashToString(x.Hash)
		mapStr["flags"] = strconv.Itoa(int(x.Flags))
		mapStr["iterations"] = strconv.Itoa(int(x.Iterations))
		mapStr["salt"] = dnsSaltToString(x.Salt)
		mapStr["type_bits"] = dnsTypeBitsMapToString(x.TypeBitMap)
		mapStr["data"] = x.NextDomain
	case *mkdns.NSEC3PARAM:
		mapStr["hash"] = dnsHashToString(x.Hash)
		mapStr["flags"] = strconv.Itoa(int(x.Flags))
		mapStr["iterations"] = strconv.Itoa(int(x.Iterations))
		mapStr["data"] = dnsSaltToString(x.Salt)
	case *mkdns.OPT: // EDNS [RFC6891]
		// OPT pseudo-RR is managed in addDnsToMapStr function
		return nil
	case *mkdns.PTR:
		mapStr["data"] = x.Ptr
	case *mkdns.RFC3597:
		// Miekg/dns lib doesn't handle this type
		debugf("Unknown RR type %s", dnsTypeToString(rrType))
		rData, err := hexStringToString(x.Rdata)
		mapStr["data"] = rData
		if err != nil {
			debugf("%s", err.Error())
		}
	case *mkdns.RRSIG:
		mapStr["type_covered"] = dnsTypeToString(x.TypeCovered)
		mapStr["algorithm"] = dnsAlgorithmToString(x.Algorithm)
		mapStr["labels"] = strconv.Itoa(int(x.Labels))
		mapStr["original_ttl"] = strconv.FormatInt(int64(x.OrigTtl), 10)
		mapStr["expiration"] = mkdns.TimeToString(x.Expiration)
		mapStr["inception"] = mkdns.TimeToString(x.Inception)
		mapStr["key_tag"] = strconv.Itoa(int(x.KeyTag))
		mapStr["signer_name"] = x.SignerName
		mapStr["data"] = x.Signature
	case *mkdns.SOA:
		mapStr["rname"] = x.Mbox
		mapStr["serial"] = x.Serial
		mapStr["refresh"] = x.Refresh
		mapStr["retry"] = x.Retry
		mapStr["expire"] = x.Expire
		mapStr["minimum"] = x.Minttl
		mapStr["data"] = x.Ns
	case *mkdns.SRV:
		mapStr["priority"] = x.Priority
		mapStr["weight"] = x.Weight
		mapStr["port"] = x.Port
		mapStr["data"] = x.Target
	case *mkdns.TXT:
		mapStr["data"] = strings.Join(x.Txt, " ")
	}

	return mapStr
}

func rrToDNSAnswer(rr mkdns.RR) *model.Answer {
	mapStr := &model.Answer{}
	rrType := rr.Header().Rrtype

	switch x := rr.(type) {
	default:
		// We don't have special handling for this type
		debugf("No special handling for RR type %s", dnsTypeToString(rrType))
		unsupportedRR := new(mkdns.RFC3597)
		err := unsupportedRR.ToRFC3597(x)
		if err == nil {
			rData, err := hexStringToString(unsupportedRR.Rdata)
			mapStr.Data = rData
			if err != nil {
				debugf("%s", err.Error())
			}
		} else {
			debugf("Rdata for the unhandled RR type %s could not be fetched", dnsTypeToString(rrType))
		}
	case *mkdns.A:
		mapStr.Data = x.A.String()
	case *mkdns.AAAA:
		mapStr.Data = x.AAAA.String()
	case *mkdns.CNAME:
		mapStr.Data = x.Target
	case *mkdns.DNSKEY:
		mapStr.Flags = strconv.Itoa(int(x.Flags))
		mapStr.Protocol = strconv.Itoa(int(x.Protocol))
		mapStr.Algorithm = dnsAlgorithmToString(x.Algorithm)
		mapStr.Data = x.PublicKey
	case *mkdns.DS:
		mapStr.KeyTag = strconv.Itoa(int(x.KeyTag))
		mapStr.Algorithm = dnsAlgorithmToString(x.Algorithm)
		mapStr.DigestType = dnsHashToString(x.DigestType)
		mapStr.Data = strings.ToUpper(x.Digest)
	case *mkdns.MX:
		mapStr.Preference = x.Preference
		mapStr.Data = x.Mx
	case *mkdns.NS:
		mapStr.Data = x.Ns
	case *mkdns.NSEC:
		mapStr.TypeBits = dnsTypeBitsMapToString(x.TypeBitMap)
		mapStr.Data = x.NextDomain
	case *mkdns.NSEC3:
		mapStr.Hash = dnsHashToString(x.Hash)
		mapStr.Flags = strconv.Itoa(int(x.Flags))
		mapStr.Iterations = strconv.Itoa(int(x.Iterations))
		mapStr.Salt = dnsSaltToString(x.Salt)
		mapStr.TypeBits = dnsTypeBitsMapToString(x.TypeBitMap)
		mapStr.Data = x.NextDomain
	case *mkdns.NSEC3PARAM:
		mapStr.Hash = dnsHashToString(x.Hash)
		mapStr.Hash = strconv.Itoa(int(x.Flags))
		mapStr.Iterations = strconv.Itoa(int(x.Iterations))
		mapStr.Data = dnsSaltToString(x.Salt)
	case *mkdns.OPT: // EDNS [RFC6891]
		// OPT pseudo-RR is managed in addDnsToMapStr function
		return nil
	case *mkdns.PTR:
		mapStr.Data = x.Ptr
	case *mkdns.RFC3597:
		// Miekg/dns lib doesn't handle this type
		debugf("Unknown RR type %s", dnsTypeToString(rrType))
		rData, err := hexStringToString(x.Rdata)
		mapStr.Data = rData
		if err != nil {
			debugf("%s", err.Error())
		}
	case *mkdns.RRSIG:
		mapStr.TypeCovered = dnsTypeToString(x.TypeCovered)
		mapStr.Algorithm = dnsAlgorithmToString(x.Algorithm)
		mapStr.Labels = strconv.Itoa(int(x.Labels))
		mapStr.OriginalTTL = strconv.FormatInt(int64(x.OrigTtl), 10)
		mapStr.Expiration = mkdns.TimeToString(x.Expiration)
		mapStr.Inception = mkdns.TimeToString(x.Inception)
		mapStr.KeyTag = strconv.Itoa(int(x.KeyTag))
		mapStr.SignerName = x.SignerName
		mapStr.Data = x.Signature
	case *mkdns.SOA:
		mapStr.Rname = x.Mbox
		mapStr.Serial = x.Serial
		mapStr.Refresh = x.Refresh
		mapStr.Retry = x.Retry
		mapStr.Expire = x.Expire
		mapStr.Minimum = x.Minttl
		mapStr.Data = x.Ns
	case *mkdns.SRV:
		mapStr.Priority = x.Priority
		mapStr.Weight = x.Weight
		mapStr.Port = x.Port
		mapStr.Data = x.Target
	case *mkdns.TXT:
		mapStr.Data = strings.Join(x.Txt, " ")
	}

	return mapStr
}

// dnsQuestionToString converts a Question to a string.
func dnsQuestionToString(q mkdns.Question) string {
	name := q.Name

	return fmt.Sprintf("class %s, type %s, %s", dnsClassToString(q.Qclass),
		dnsTypeToString(q.Qtype), name)
}

// rrsToString converts an array of RR's to a
// string.
func rrsToString(r []mkdns.RR) string {
	var rrStrs []string
	for _, rr := range r {
		rrStrs = append(rrStrs, rrToString(rr))
	}
	return strings.Join(rrStrs, "; ")
}

// decodeDnsData decodes a byte array into a DNS struct. If an error occurs
// then the returned dns pointer will be nil. This method recovers from panics
// and is concurrency-safe.
// We do not handle Unpack ErrTruncated for now. See https://github.com/miekg/dns/pull/281
func decodeDNSData(transp transport, rawData []byte) (dns *mkdns.Msg, err error) {
	var offset int
	if transp == transportTCP {
		offset = decodeOffset
	}

	// Recover from any panics that occur while parsing a packet.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	msg := &mkdns.Msg{}
	err = msg.Unpack(rawData[offset:])

	// Message should be more than 12 bytes.
	// The 12 bytes value corresponds to a message header length.
	// We use this check because Unpack does not return an error for some unvalid messages.
	// TODO: can a better solution be found?
	if msg.Len() <= 12 || err != nil {
		return nil, nonDNSMsg
	}
	return msg, nil
}

// Decode a byte array into a DNS struct
// decodeDNSHeader decode the DNS header message only
func decodeDNSHeader(transp transport, rawData []byte) (dns *mkdns.Msg, err error) {
	var offset int
	qrBit := 12
	if len(rawData) < qrBit {
		qrBit = len(rawData)
	}
	if transp == transportTCP {
		offset = decodeOffset
	}

	// Recover from any panics that occur while parsing a packet.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	msg := &mkdns.Msg{}
	err = msg.Unpack(rawData[offset:qrBit])
	return msg, nil
}

//Capture case Format Error
func handleErrorMsg(srcIP, dstIP string, transp transport, rawData []byte, tuple common.IPPortTuple) {
	dnsHdr, _ := decodeDNSHeader(transp, rawData)
	if dnsHdr.Response {
		if dnsHdr.MsgHdr.Truncated == true {
			// listTruncateRespID[dnsHdr.MsgHdr.Id] = true
			statsdns.HandleResponseTruncated(dstIP, srcIP)
		} else {
			statsdns.HandleResponseDecodeErr(dstIP, srcIP, dnsResponseCodeToString(dnsHdr.MsgHdr.Rcode))
		}
	} else {
		// statsdns.CreateCounterMetric(srcIP, dstIP)
		statsdns.HandleRequestDecodeErr(srcIP, dstIP)
	}
}
