package dicom

import (
	"encoding/binary"
)

// itemRoleSelection is the SCP/SCU role selection negotiation sub-item.
//
// It lives inside the user information item alongside the maximum PDU length, not beside the presentation contexts, which is
// why it is easy to miss when reading the encoder: a context says what kind of object may cross the association and this
// says who is allowed to initiate it.
const itemRoleSelection = 0x54

// RoleSelection proposes who acts as service class user and provider for a SOP class.
//
// Needed for exactly one thing, and that thing is C-GET. In a C-GET the archive sends the images back as C-STORE requests on
// the same association it was asked on, which inverts the usual arrangement: we initiated the association, and for those
// stores we are the provider rather than the user.
//
// An archive cannot infer that. Without this sub-item it has no way to know we would accept a store, so it either refuses
// the C-GET or accepts it and returns zero images - and a retrieve that succeeds having transferred nothing is a far worse
// outcome than one that fails.
//
// This is the reason C-MOVE exists and is still common despite being more awkward operationally: C-MOVE needs no role
// negotiation because the images go to a separate listener over a separate association.
type RoleSelection struct {
	// SOPClass is the abstract syntax this applies to.
	SOPClass string

	// SCURole says the proposer will act as the service class user.
	SCURole bool

	// SCPRole says the proposer will act as the service class provider - for C-GET, that it will accept stores.
	SCPRole bool
}

// encodeRoleSelection builds one role selection sub-item.
func encodeRoleSelection(r RoleSelection) []byte {
	uid := []byte(r.SOPClass)

	var inner []byte
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(uid)))
	inner = append(inner, length...)
	inner = append(inner, uid...)
	inner = append(inner, boolByte(r.SCURole), boolByte(r.SCPRole))

	return encodeItem(itemRoleSelection, inner)
}

// decodeRoleSelection reads one role selection sub-item's contents.
//
// Returns false rather than an error on anything malformed. Role selection is negotiation: an acceptor that answered
// unintelligibly has declined, and treating that as a protocol failure would abort an association that could still do
// everything else.
func decodeRoleSelection(data []byte) (RoleSelection, bool) {
	if len(data) < 4 {
		return RoleSelection{}, false
	}

	uidLen := int(binary.BigEndian.Uint16(data[0:2]))
	if uidLen < 0 || 2+uidLen+2 > len(data) {
		return RoleSelection{}, false
	}

	return RoleSelection{
		SOPClass: trimUID(string(data[2 : 2+uidLen])),
		SCURole:  data[2+uidLen] != 0,
		SCPRole:  data[2+uidLen+1] != 0,
	}, true
}

func boolByte(b bool) byte {
	if b {
		return 0x01
	}
	return 0x00
}

// trimUID removes the padding a UID may arrive with.
//
// A UID of odd length is padded with a null, and one that is not is sometimes padded with a space anyway. Both have to go, or
// a comparison against a constant fails on a UID that is visibly identical in a log.
func trimUID(s string) string {
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != 0x00 && last != ' ' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
