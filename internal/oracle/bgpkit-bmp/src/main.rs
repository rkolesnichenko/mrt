//! bgpkit-bmp FILE: the elems bgpkit-parser 0.22.0 reads from a BMP stream
//! (FILE ending in .bmp) or an OpenBMP frame stream (.obmp), one
//! `json!(elem)` per line on standard output, the form `bgpkit-parser --json`
//! prints, so internal/oracle's NormalizeBgpkit reads it.
//!
//! Only Route Monitoring messages give elems, through
//! `Elementor::bgp_to_elems` with the per-peer header's timestamp, address
//! and AS. A message bgpkit-parser rejects goes to standard error as
//! `skip: OFFSET: ERROR` and costs only that message: the harness frames the
//! stream itself, by the BMP common header's length (or the OBMP header and
//! message lengths), before calling the library. A stream that ends inside a
//! message is one last skip. The exit status is 0 unless FILE cannot be read
//! or the output cannot be written.

use bgpkit_parser::parser::bmp::messages::BmpMessageBody;
use bgpkit_parser::{parse_bmp_msg, parse_openbmp_header, Elementor};
use bytes::Bytes;
use serde_json::json;
use std::io::Write;
use std::process::ExitCode;

const BMP_HEADER_LEN: usize = 6;
const OBMP_PREFIX_LEN: usize = 12; // magic, version, header length, message length

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().collect();
    if args.len() == 2 && args[1] == "--version" {
        println!("bgpkit-bmp {}", env!("CARGO_PKG_VERSION"));
        return ExitCode::SUCCESS;
    }
    if args.len() != 2 {
        eprintln!("usage: bgpkit-bmp FILE.bmp|FILE.obmp");
        return ExitCode::from(2);
    }
    let path = &args[1];
    let data = match std::fs::read(path) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("bgpkit-bmp: {path}: {e}");
            return ExitCode::FAILURE;
        }
    };
    let obmp = path.ends_with(".obmp");
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    let mut off = 0;
    while off < data.len() {
        let rest = &data[off..];
        let len = if obmp {
            obmp_frame_len(rest)
        } else {
            bmp_message_len(rest)
        };
        let Some(len) = len.filter(|&n| n <= rest.len()) else {
            eprintln!("skip: {off}: no whole message follows; the rest is not read");
            break;
        };
        match elems(Bytes::copy_from_slice(&rest[..len]), obmp) {
            Ok(lines) => {
                for l in lines {
                    if let Err(e) = writeln!(out, "{l}") {
                        eprintln!("bgpkit-bmp: {e}");
                        return ExitCode::FAILURE;
                    }
                }
            }
            Err(e) => eprintln!("skip: {off}: {e}"),
        }
        off += len;
    }
    if let Err(e) = out.flush() {
        eprintln!("bgpkit-bmp: {e}");
        return ExitCode::FAILURE;
    }
    ExitCode::SUCCESS
}

/// The length of the BMP message at the front of b: its common header's
/// Message Length, which counts the header (RFC 7854 section 4.1). A length
/// below 6 cannot hold the header; its 6 bytes are taken as one message, as
/// package bmp's reader does.
fn bmp_message_len(b: &[u8]) -> Option<usize> {
    if b.len() < BMP_HEADER_LEN {
        return None;
    }
    let n = u32::from_be_bytes([b[1], b[2], b[3], b[4]]) as usize;
    Some(n.max(BMP_HEADER_LEN))
}

/// The length of the OBMP frame at the front of b: its header length plus
/// its message length. A frame has no resync point, so one shorter than the
/// fixed prefix ends the stream, as in package openbmp's reader.
fn obmp_frame_len(b: &[u8]) -> Option<usize> {
    if b.len() < OBMP_PREFIX_LEN {
        return None;
    }
    let header = u16::from_be_bytes([b[6], b[7]]) as usize;
    let msg = u32::from_be_bytes([b[8], b[9], b[10], b[11]]) as usize;
    Some(header + msg).filter(|&n| n >= OBMP_PREFIX_LEN)
}

/// Parses one message (an OBMP frame when obmp is set) and returns the elems
/// of a Route Monitoring as JSON lines, none for any other message.
fn elems(mut b: Bytes, obmp: bool) -> Result<Vec<String>, String> {
    let mut collector_time = 0.0;
    if obmp {
        let h = parse_openbmp_header(&mut b).map_err(|e| format!("openbmp: {e}"))?;
        collector_time = h.timestamp;
    }
    let msg = parse_bmp_msg(&mut b).map_err(|e| e.to_string())?;
    let (Some(peer), BmpMessageBody::RouteMonitoring(m)) = (msg.per_peer_header, msg.message_body)
    else {
        return Ok(Vec::new());
    };
    // As bgpkit-parser's OpenBMP example does: the per-peer time, else the
    // collector's.
    let ts = if peer.timestamp > 0.0 {
        peer.timestamp
    } else {
        collector_time
    };
    Ok(
        Elementor::bgp_to_elems(m.bgp_message, ts, &peer.peer_ip, &peer.peer_asn)
            .iter()
            .map(|elem| json!(elem).to_string())
            .collect(),
    )
}
