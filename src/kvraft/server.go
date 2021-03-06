package raftkv

import (
	"bytes"
	"encoding/gob"
	"labrpc"
	"log"
	"raft"
	"sync"
	"time"
)

const Debug = 0

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}

const TIMEOUT = 600

type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	Type    string
	Key     string
	Value   string
	ClerkId uint64
	Seq     uint64
}

type WaitingOp struct {
	WaitCh  chan bool
	ClerkId uint64
	OpSeq   uint64
}

type RaftKV struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	KVMap    map[string]string
	OpSeqMap map[uint64]uint64
	waitOps  map[int][]*WaitingOp
}

func (kv *RaftKV) Get(args *GetArgs, reply *GetReply) {
	// Your code here.
	op := Op{}
	op.Key = args.Key
	op.ClerkId = args.ClerkId
	op.Seq = args.OpSeq
	op.Type = GET

	DPrintf("KVRaft %v Get handler call raft start op: %v", kv.me, op)
	index, _, isLeader := kv.rf.Start(op)
	DPrintf("KVRaft %v Get handler call raft start op: %v", kv.me, op)
	waiter := WaitingOp{
		WaitCh:  make(chan bool, 1),
		ClerkId: args.ClerkId,
		OpSeq:   args.OpSeq,
	}
	kv.mu.Lock()
	kv.waitOps[index] = append(kv.waitOps[index], &waiter)
	kv.mu.Unlock()

	timer := time.NewTimer(time.Millisecond * TIMEOUT)
	var ok bool
	if !isLeader {
		reply.WrongLeader = true
	} else {
		reply.WrongLeader = false
		select {
		case ok = <-waiter.WaitCh:
			if ok {
				kv.mu.Lock()
				if val, ok := kv.KVMap[args.Key]; ok {
					reply.Err = OK
					reply.Value = val
				} else {
					reply.Err = ErrNoKey
				}
				kv.mu.Unlock()
			} else {
				reply.Err = ErrLeaderChange
			}
		case <-timer.C:
			reply.Err = ErrTimeOut
		}
	}
}

func (kv *RaftKV) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	op := Op{}
	op.Key = args.Key
	op.Value = args.Value
	op.ClerkId = args.ClerkId
	op.Seq = args.OpSeq
	op.Type = args.OpType

	DPrintf("KVRaft %v PutAppend handler call raft start op: %v", kv.me, op)
	index, _, isLeader := kv.rf.Start(op)
	DPrintf("KVRaft %v PutAppend handler call raft start return index:%v isleader:%v", kv.me, index, isLeader)
	waiter := WaitingOp{
		WaitCh:  make(chan bool, 1),
		ClerkId: args.ClerkId,
		OpSeq:   args.OpSeq,
	}
	kv.mu.Lock()
	kv.waitOps[index] = append(kv.waitOps[index], &waiter)
	kv.mu.Unlock()

	timer := time.NewTimer(time.Millisecond * TIMEOUT)
	var ok bool
	if !isLeader {
		reply.WrongLeader = true
	} else {
		reply.WrongLeader = false
		select {
		case ok = <-waiter.WaitCh:
			if ok {
				reply.Err = OK
			} else {
				reply.Err = ErrLeaderChange
			}
		case <-timer.C:
			reply.Err = ErrTimeOut
		}
	}
}

//
// the tester calls Kill() when a RaftKV instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (kv *RaftKV) Kill() {
	kv.rf.Kill()
	// Your code here, if desired.
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots with persister.SaveSnapshot(),
// and Raft should save its state (including log) with persister.SaveRaftState().
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *RaftKV {
	// call gob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	gob.Register(Op{})

	kv := new(RaftKV)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// You may need initialization code here.
	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)
	kv.KVMap = make(map[string]string)
	kv.OpSeqMap = make(map[uint64]uint64)
	kv.waitOps = make(map[int][]*WaitingOp)
	kv.ReadSnapshot()
	// You may need initialization code here.
	go func() {
		for {
			msg := <-kv.applyCh
			kv.ExeuteApplyMsg(msg)
		}
	}()

	return kv
}

func (kv *RaftKV) ExeuteApplyMsg(msg raft.ApplyMsg) {
	DPrintf("Raftkv:%v Enter Exe msg:%v", kv.me, msg)
	kv.mu.Lock()
	defer kv.mu.Unlock()
	DPrintf("Raftkv:%v Exe msg:%v", kv.me, msg)
	if msg.UseSnapshot {
		DPrintf("recieve snapshot update")
		data := msg.Snapshot
		kv.UpdateBySnapshot(data)
	} else {
		op, succ := msg.Command.(Op)
		//that means this msg is from a dummy log generated when loading snapshot.
		//the purpose is to make Raft.Logs not empty except after just started
		if !succ {
			return
		}

		if seq, ok := kv.OpSeqMap[op.ClerkId]; !ok || seq < op.Seq {
			switch op.Type {
			//'get' don't need to do anything
			case PUT:
				kv.KVMap[op.Key] = op.Value
			case APPEND:
				kv.KVMap[op.Key] = kv.KVMap[op.Key] + op.Value
			}

			kv.OpSeqMap[op.ClerkId] = op.Seq

			if kv.maxraftstate != -1 && kv.maxraftstate <= kv.rf.RaftStateSize() {
				data := kv.encodeKVData(msg.Index, msg.Term)
				go func(index int, data []byte) {
					kv.SaveSnapshot(index, data)
				}(msg.Index, data)
			}
		}

		if waiters, ok := kv.waitOps[msg.Index]; ok {
			for _, w := range waiters {
				if w.ClerkId == op.ClerkId && w.OpSeq == op.Seq {
					w.WaitCh <- true
				} else {
					w.WaitCh <- false
				}
			}
			delete(kv.waitOps, msg.Index)
		}

		DPrintf("KvRaft %v Exe msg %v finish!!", kv.me, msg)
	}
}

func (kv *RaftKV) SaveSnapshot(index int, data []byte) {
	DPrintf("save snapshot")
	DPrintf("call rf save snapshot")
	kv.rf.SaveSnapshot(data, index)
	DPrintf("save snapshot finish")
}

func (kv *RaftKV) encodeKVData(index int, term int) []byte {
	buf := new(bytes.Buffer)
	encoder := gob.NewEncoder(buf)
	encoder.Encode(kv.KVMap)
	encoder.Encode(kv.OpSeqMap)
	encoder.Encode(index)
	encoder.Encode(term)
	data := buf.Bytes()
	return data
}

func (kv *RaftKV) ReadSnapshot() {
	DPrintf("read snapshot")
	data := kv.rf.ReadSnapshot()
	kv.UpdateBySnapshot(data)
}

func (kv *RaftKV) UpdateBySnapshot(data []byte) {
	DPrintf("update by snapshot")
	if data == nil || len(data) < 1 {
		DPrintf("have no snapshot, return")
	}
	buf := bytes.NewBuffer(data)
	decoder := gob.NewDecoder(buf)
	decoder.Decode(&kv.KVMap)
	decoder.Decode(&kv.OpSeqMap)
}
