// Copyright 2019 BlueCat Networks (USA) Inc. and its affiliates
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
package ip6defrag

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tsg/gopacket"
	"github.com/tsg/gopacket/bytediff"
	"github.com/tsg/gopacket/layers"
)

func BenchmarkDefrag(b *testing.B) {
	defrag := NewIPv6Defragmenter()
	for i := 0; i < b.N; i++ {
		gentestBench(b, defrag, testBCDNSFrag1)
		gentestBench(b, defrag, testBCDNSFrag2)
	}
}

func gentestBench(b *testing.B, defrag *IPv6Defragmenter, buf []byte) {
	p := gopacket.NewPacket(buf, layers.LinkTypeEthernet, gopacket.Default)
	if p.ErrorLayer() != nil {
		b.Error("Failed to decode packet:", p.ErrorLayer().Error())
	}

	ipL := p.Layer(layers.LayerTypeIPv6)
	if ipL == nil {
		b.Fatal("Failed to get ipv6 from layers")
	}
	ip, _ := ipL.(*layers.IPv6)

	_, err := defrag.DefragIPv6(ip)
	if err != nil {
		b.Fatalf("defrag: %s", err)
	}
}

func TestDefragBCDNS(t *testing.T) {
	defrag := NewIPv6Defragmenter()

	gentestDefrag(t, defrag, testBCDNSFrag1, false, "BCDNSFrag1")
	ip := gentestDefrag(t, defrag, testBCDNSFrag2, true, "BCDNSFrag2")

	if len(ip.Payload) != 1499 {
		t.Fatalf("defrag: expecting a packet of 1499 bytes, got %d", len(ip.Payload))
	}

	validPayload := append(testBCDNSFrag1[54+8:], testBCDNSFrag2[54+8:]...)
	if bytes.Compare(validPayload, ip.Payload) != 0 {
		fmt.Println(bytediff.BashOutput.String(
			bytediff.Diff(validPayload, ip.Payload)))
		t.Errorf("defrag: payload is not correctly defragmented")
	}
}

func TestDefragBCDNSOutOfOrder(t *testing.T) {
	defrag := NewIPv6Defragmenter()

	// packet 2 comes first
	gentestDefrag(t, defrag, testBCDNSFrag2, false, "BCDNSFrag2")
	ip := gentestDefrag(t, defrag, testBCDNSFrag1, true, "BCDNSFrag1")

	if len(ip.Payload) != 1499 {
		t.Fatalf("defrag: expecting a packet of 1499 bytes, got %d", len(ip.Payload))
	}

	validPayload := append(testBCDNSFrag1[54+8:], testBCDNSFrag2[54+8:]...)
	if bytes.Compare(validPayload, ip.Payload) != 0 {
		fmt.Println(bytediff.BashOutput.String(
			bytediff.Diff(validPayload, ip.Payload)))
		t.Errorf("defrag: payload is not correctly defragmented")
	}
}

func TestDontFragBCDNS(t *testing.T) {
	defrag := NewIPv6Defragmenter()

	ip := gentestDefrag(t, defrag, testBCDNSDontFrag, true, "BCDNSDontFrag")

	if len(ip.Payload) != 67 {
		t.Fatalf("defrag: expecting a packet of 67 bytes, got %d", len(ip.Payload))
	}

	validPayload := testBCDNSDontFrag[54:]
	if bytes.Compare(validPayload, ip.Payload) != 0 {
		fmt.Println(bytediff.BashOutput.String(
			bytediff.Diff(validPayload, ip.Payload)))
		t.Errorf("defrag: payload is not correctly defragmented")
	}
}

func gentestDefrag(t *testing.T, defrag *IPv6Defragmenter, buf []byte, expect bool, label string) *layers.IPv6 {
	p := gopacket.NewPacket(buf, layers.LinkTypeEthernet, gopacket.Default)
	if p.ErrorLayer() != nil {
		t.Error("Failed to decode packet:", p.ErrorLayer().Error())
	}

	ipL := p.Layer(layers.LayerTypeIPv6)
	if ipL == nil {
		t.Fatal("Failed to get ipv6 from layers")
	}
	ip, _ := ipL.(*layers.IPv6)

	out, err := defrag.DefragIPv6(ip)
	if err != nil {
		t.Fatalf("defrag: %s", err)
	}
	status := false
	if out != nil {
		status = true
	}
	if status != expect {
		t.Fatalf("defrag: a fragment was not detected (%s)", label)
	}
	return out
}

var testBCDNSFrag1 = []byte{
	0x00, 0x0c, 0x29, 0xec, 0x83, 0x48, 0x00, 0x0c, 0x29, 0x91, 0xb8, 0x19, 0x86, 0xdd, 0x60, 0x05, //  ..)ì.H..).¸..Ý`.
	0x12, 0xe5, 0x05, 0xb0, 0x2c, 0x40, 0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x0c, //  .å.°,@þ.........
	0x29, 0xff, 0xfe, 0x91, 0xb8, 0x19, 0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xb2, 0xd2, //  )ÿþ.¸.þ.......²Ò
	0xc8, 0xc8, 0xed, 0x81, 0x75, 0xa2, 0x11, 0x00, 0x00, 0x01, 0x76, 0x14, 0x35, 0x04, 0x00, 0x35, //  ÈÈí.u¢....v.5..5
	0xd0, 0x81, 0x05, 0xdb, 0x9f, 0x73, 0x08, 0x1f, 0x85, 0x80, 0x00, 0x01, 0x00, 0x06, 0x00, 0x01, //  Ð..Û.s..........
	0x00, 0x01, 0x12, 0x74, 0x65, 0x73, 0x74, 0x2d, 0x6e, 0x61, 0x70, 0x74, 0x72, 0x2d, 0x72, 0x65, //  ...test-naptr-re
	0x63, 0x6f, 0x72, 0x64, 0x33, 0x07, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, //  cord3.example.co
	0x6d, 0x00, 0x00, 0x23, 0x00, 0x01, 0xc0, 0x0c, 0x00, 0x23, 0x00, 0x01, 0x00, 0x00, 0x0e, 0x10, //  m..#..À..#......
	0x00, 0xdd, 0x00, 0x00, 0x00, 0x00, 0x01, 0x53, 0x0a, 0x6d, 0x79, 0x2d, 0x73, 0x65, 0x72, 0x76, //  .Ý.....S.my-serv
	0x69, 0x63, 0x65, 0x3e, 0x21, 0x5e, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, //  ice>!^abcdefghij
	0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72, 0x62, 0x62, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, //  klmnopqrbb123456
	0x37, 0x38, 0x39, 0x30, 0x21, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, //  7890!ABCDEFGHIJK
	0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, //  LMNOPQR123456789
	0x30, 0x21, 0x08, 0x74, 0x65, 0x73, 0x74, 0x68, 0x6f, 0x73, 0x74, 0x0a, 0x62, 0x31, 0x31, 0x31, //  0!.testhost.b111
	0x31, 0x31, 0x31, 0x31, 0x31, 0x62, 0x0b, 0x62, 0x62, 0x62, 0x62, 0x62, 0x62, 0x62, 0x62, 0x62, //  11111b.bbbbbbbbb
	0x62, 0x62, 0x0b, 0x68, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x68, 0x0b, 0x67, //  bb.h123456789h.g
	0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x67, 0x0b, 0x66, 0x31, 0x32, 0x33, 0x34, //  123456789g.f1234
	0x35, 0x36, 0x37, 0x38, 0x39, 0x66, 0x0b, 0x65, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, //  56789f.e12345678
	0x39, 0x65, 0x0b, 0x64, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x64, 0x0b, 0x63, //  9e.d123456789d.c
	0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x63, 0x0b, 0x62, 0x31, 0x32, 0x33, 0x34, //  123456789c.b1234
	0x35, 0x36, 0x37, 0x38, 0x39, 0x62, 0x0b, 0x61, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, //  56789b.a12345678
	0x39, 0x61, 0x07, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0xc0, //  9a.example.com.À
	0x0c, 0x00, 0x23, 0x00, 0x01, 0x00, 0x00, 0x0e, 0x10, 0x00, 0xdd, 0x00, 0x00, 0x00, 0x00, 0x01, //  ..#.......Ý.....
	0x53, 0x0a, 0x6d, 0x79, 0x2d, 0x73, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x3e, 0x21, 0x5e, 0x61, //  S.my-service>!^a
	0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, //  bcdefghijklmnopq
	0x72, 0x65, 0x65, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, 0x41, 0x42, //  ree1234567890!AB
	0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, //  CDEFGHIJKLMNOPQR
	0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, 0x08, 0x74, 0x65, 0x73, 0x74, //  1234567890!.test
	0x68, 0x6f, 0x73, 0x74, 0x0a, 0x65, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x65, 0x0b, //  host.e11111111e.
	0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x0b, 0x68, 0x31, 0x32, 0x33, //  eeeeeeeeeee.h123
	0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x68, 0x0b, 0x67, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, //  456789h.g1234567
	0x38, 0x39, 0x67, 0x0b, 0x66, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x66, 0x0b, //  89g.f123456789f.
	0x65, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x65, 0x0b, 0x64, 0x31, 0x32, 0x33, //  e123456789e.d123
	0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x64, 0x0b, 0x63, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, //  456789d.c1234567
	0x38, 0x39, 0x63, 0x0b, 0x62, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x62, 0x0b, //  89c.b123456789b.
	0x61, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x61, 0x07, 0x65, 0x78, 0x61, 0x6d, //  a123456789a.exam
	0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0xc0, 0x0c, 0x00, 0x23, 0x00, 0x01, 0x00, 0x00, //  ple.com.À..#....
	0x0e, 0x10, 0x00, 0xdd, 0x00, 0x00, 0x00, 0x00, 0x01, 0x53, 0x0a, 0x6d, 0x79, 0x2d, 0x73, 0x65, //  ...Ý.....S.my-se
	0x72, 0x76, 0x69, 0x63, 0x65, 0x3e, 0x21, 0x5e, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, //  rvice>!^abcdefgh
	0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72, 0x64, 0x64, 0x31, 0x32, 0x33, 0x34, //  ijklmnopqrdd1234
	0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, //  567890!ABCDEFGHI
	0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, //  JKLMNOPQR1234567
	0x38, 0x39, 0x30, 0x21, 0x08, 0x74, 0x65, 0x73, 0x74, 0x68, 0x6f, 0x73, 0x74, 0x0a, 0x64, 0x31, //  890!.testhost.d1
	0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x64, 0x0b, 0x64, 0x64, 0x64, 0x64, 0x64, 0x64, 0x64, //  1111111d.ddddddd
	0x64, 0x64, 0x64, 0x64, 0x0b, 0x68, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x68, //  dddd.h123456789h
	0x0b, 0x67, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x67, 0x0b, 0x66, 0x31, 0x32, //  .g123456789g.f12
	0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x66, 0x0b, 0x65, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, //  3456789f.e123456
	0x37, 0x38, 0x39, 0x65, 0x0b, 0x64, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x64, //  789e.d123456789d
	0x0b, 0x63, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x63, 0x0b, 0x62, 0x31, 0x32, //  .c123456789c.b12
	0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x62, 0x0b, 0x61, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, //  3456789b.a123456
	0x37, 0x38, 0x39, 0x61, 0x07, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, //  789a.example.com
	0x00, 0xc0, 0x0c, 0x00, 0x23, 0x00, 0x01, 0x00, 0x00, 0x0e, 0x10, 0x00, 0xdd, 0x00, 0x00, 0x00, //  .À..#.......Ý...
	0x00, 0x01, 0x53, 0x0a, 0x6d, 0x79, 0x2d, 0x73, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x3e, 0x21, //  ..S.my-service>!
	0x5e, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, //  ^abcdefghijklmno
	0x70, 0x71, 0x72, 0x61, 0x61, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, //  pqraa1234567890!
	0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, //  ABCDEFGHIJKLMNOP
	0x51, 0x52, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, 0x08, 0x74, 0x65, //  QR1234567890!.te
	0x73, 0x74, 0x68, 0x6f, 0x73, 0x74, 0x0a, 0x61, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, //  sthost.a11111111
	0x61, 0x0b, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x0b, 0x68, 0x31, //  a.aaaaaaaaaaa.h1
	0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x68, 0x0b, 0x67, 0x31, 0x32, 0x33, 0x34, 0x35, //  23456789h.g12345
	0x36, 0x37, 0x38, 0x39, 0x67, 0x0b, 0x66, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, //  6789g.f123456789
	0x66, 0x0b, 0x65, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x65, 0x0b, 0x64, 0x31, //  f.e123456789e.d1
	0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x64, 0x0b, 0x63, 0x31, 0x32, 0x33, 0x34, 0x35, //  23456789d.c12345
	0x36, 0x37, 0x38, 0x39, 0x63, 0x0b, 0x62, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, //  6789c.b123456789
	0x62, 0x0b, 0x61, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x61, 0x07, 0x65, 0x78, //  b.a123456789a.ex
	0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0xc0, 0x0c, 0x00, 0x23, 0x00, 0x01, //  ample.com.À..#..
	0x00, 0x00, 0x0e, 0x10, 0x00, 0xe7, 0x00, 0x00, 0x00, 0x00, 0x01, 0x53, 0x0a, 0x6d, 0x79, 0x2d, //  .....ç.....S.my-
	0x73, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x40, 0x21, 0x5e, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, //  service@!^abcdef
	0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72, 0x61, 0x61, 0x31, 0x31, //  ghijklmnopqraa11
	0x32, 0x32, 0x33, 0x33, 0x34, 0x34, 0x31, 0x31, 0x30, 0x21, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, //  223344110!ABCDEF
	0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, 0x31, 0x31, 0x32, 0x32, //  GHIJKLMNOPQR1122
	0x33, 0x33, 0x34, 0x34, 0x31, 0x31, 0x30, 0x21, 0x08, 0x74, 0x65, 0x73, 0x74, 0x68, 0x6f, 0x73, //  3344110!.testhos
	0x74, 0x0a, 0x61, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x61, 0x0b, 0x61, 0x61, 0x61, //  t.a11111111a.aaa
	0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x0c, 0x68, 0x31, 0x31, 0x32, 0x32, 0x33, 0x33, //  aaaaaaaa.h112233
	0x34, 0x34, 0x31, 0x31, 0x68, 0x0c, 0x67, 0x31, 0x31, 0x32, 0x32, 0x33, 0x33, 0x34, 0x34, 0x31, //  4411h.g112233441
	0x31, 0x67, 0x0c, 0x66, 0x31, 0x31, 0x32, 0x32, 0x33, 0x33, 0x34, 0x34, 0x31, 0x31, 0x66, 0x0c, //  1g.f1122334411f.
	0x65, 0x31, 0x31, 0x32, 0x32, 0x33, 0x33, 0x34, 0x34, 0x31, 0x31, 0x65, 0x0c, 0x64, 0x31, 0x31, //  e1122334411e.d11
	0x32, 0x32, 0x33, 0x33, 0x34, 0x34, 0x31, 0x31, 0x64, 0x0c, 0x63, 0x31, 0x31, 0x32, 0x32, 0x33, //  22334411d.c11223
	0x33, 0x34, 0x34, 0x31, 0x31, 0x63, 0x0c, 0x62, 0x31, 0x31, 0x32, 0x32, 0x33, 0x33, 0x34, 0x34, //  34411c.b11223344
	0x31, 0x31, 0x62, 0x0c, 0x61, 0x31, 0x31, 0x32, 0x32, 0x33, 0x33, 0x34, 0x34, 0x31, 0x31, 0x61, //  11b.a1122334411a
	0x07, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0xc0, 0x0c, 0x00, //  .example.com.À..
	0x23, 0x00, 0x01, 0x00, 0x00, 0x0e, 0x10, 0x00, 0xdd, 0x00, 0x00, 0x00, 0x00, 0x01, 0x53, 0x0a, //  #.......Ý.....S.
	0x6d, 0x79, 0x2d, 0x73, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x3e, 0x21, 0x5e, 0x61, 0x62, 0x63, //  my-service>!^abc
	0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72, 0x63, //  defghijklmnopqrc
	0x63, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, 0x41, 0x42, 0x43, 0x44, //  c1234567890!ABCD
	0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, 0x31, 0x32, //  EFGHIJKLMNOPQR12
	0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x30, 0x21, 0x08, 0x74, 0x65, 0x73, 0x74, 0x68, 0x6f, //  34567890!.testho
	0x73, 0x74, 0x0a, 0x63, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x31, 0x63, 0x0b, 0x63, 0x63, //  st.c11111111c.cc
	0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x0b, 0x68, 0x31, 0x32, 0x33, 0x34, 0x35, //  ccccccccc.h12345
	0x36, 0x37, 0x38, 0x39, 0x68, 0x0b, 0x67, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, //  6789h.g123456789
	0x67, 0x0b, 0x66, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x66, 0x0b, 0x65, 0x31, //  g.f123456789f.e1
	0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x65, 0x0b, 0x64, 0x31, 0x32, 0x33, 0x34, 0x35, //  23456789e.d12345
	0x36, 0x37, 0x38, 0x39, 0x64, 0x0b, 0x63, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, //  6789d.c123456789
	0x63, 0x0b, 0x62, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x62, 0x0b, 0x61, 0x31, //  c.b123456789b.a1
	0x32, 0x33, 0x34, 0x35, 0x36, 0x37, //  234567
}

var testBCDNSFrag2 = []byte{
	0x00, 0x0c, 0x29, 0xec, 0x83, 0x48, 0x00, 0x0c, 0x29, 0x91, 0xb8, 0x19, 0x86, 0xdd, 0x60, 0x05, //  ..)ì.H..).¸..Ý`.
	0x12, 0xe5, 0x00, 0x3b, 0x2c, 0x40, 0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x0c, //  .å.;,@þ.........
	0x29, 0xff, 0xfe, 0x91, 0xb8, 0x19, 0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xb2, 0xd2, //  )ÿþ.¸.þ.......²Ò
	0xc8, 0xc8, 0xed, 0x81, 0x75, 0xa2, 0x11, 0x00, 0x05, 0xa8, 0x76, 0x14, 0x35, 0x04, 0x38, 0x39, //  ÈÈí.u¢...¨v.5.89
	0x61, 0x07, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0xc5, 0xa3, //  a.example.com.Å£
	0x00, 0x02, 0x00, 0x01, 0x00, 0x01, 0x51, 0x80, 0x00, 0x0c, 0x0a, 0x62, 0x64, 0x64, 0x73, 0x38, //  ......Q....bdds8
	0x38, 0x2d, 0x32, 0x33, 0x39, 0x00, 0x00, 0x00, 0x29, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, //  8-239...).......
	0x00, //  .
}

var testBCDNSDontFrag = []byte{
	0x00, 0x0c, 0x29, 0x91, 0xb8, 0x19, 0x00, 0x0c, 0x29, 0xec, 0x83, 0x48, 0x86, 0xdd, 0x60, 0x0f, //  ..).¸...)ì.H.Ý`.
	0x3f, 0x15, 0x00, 0x43, 0x11, 0x40, 0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xb2, 0xd2, //  ?..C.@þ.......²Ò
	0xc8, 0xc8, 0xed, 0x81, 0x75, 0xa2, 0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x0c, //  ÈÈí.u¢þ.........
	0x29, 0xff, 0xfe, 0x91, 0xb8, 0x19, 0xd0, 0x81, 0x00, 0x35, 0x00, 0x43, 0xbe, 0xcc, 0x08, 0x1f, //  )ÿþ.¸.Ð..5.C¾Ì..
	0x01, 0x20, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x12, 0x74, 0x65, 0x73, 0x74, 0x2d, //  . .........test-
	0x6e, 0x61, 0x70, 0x74, 0x72, 0x2d, 0x72, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x33, 0x07, 0x65, 0x78, //  naptr-record3.ex
	0x61, 0x6d, 0x70, 0x6c, 0x65, 0x03, 0x63, 0x6f, 0x6d, 0x00, 0x00, 0x23, 0x00, 0x01, 0x00, 0x00, //  ample.com..#....
	0x29, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, //  )........
}