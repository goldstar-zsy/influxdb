package tests

import (
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fmt"
	"github.com/influxdata/influxdb/cmd/influxd/backup"
	"github.com/influxdata/influxdb/cmd/influxd/restore"
)

func TestServer_BackupAndRestore(t *testing.T) {
	config := NewConfig()
	config.Data.Engine = "tsm1"
	config.Data.Dir, _ = ioutil.TempDir("", "data_backup")
	config.Meta.Dir, _ = ioutil.TempDir("", "meta_backup")
	config.BindAddress = freePort()

	fullBackupDir, _ := ioutil.TempDir("", "backup")
	defer os.RemoveAll(fullBackupDir)

	partialBackupDir, _ := ioutil.TempDir("", "backup")
	defer os.RemoveAll(fullBackupDir)

	db := "mydb"
	rp := "forever"
	expected := `{"results":[{"statement_id":0,"series":[{"name":"myseries","columns":["time","host","value"],"values":[["1970-01-01T00:00:00.001Z","A",23],["1970-01-01T00:00:00.005Z","B",24],["1970-01-01T00:00:00.009Z","C",25]]}]}]}`
	partialExpected := `{"results":[{"statement_id":0,"series":[{"name":"myseries","columns":["time","host","value"],"values":[["1970-01-01T00:00:00.001Z","A",23],["1970-01-01T00:00:00.005Z","B",24]]}]}]}`

	// set the cache snapshot size low so that a single point will cause TSM file creation
	config.Data.CacheSnapshotMemorySize = 1

	func() {
		s := OpenServer(config)
		defer s.Close()

		if _, ok := s.(*RemoteServer); ok {
			t.Skip("Skipping.  Cannot modify remote server config")
		}

		if err := s.CreateDatabaseAndRetentionPolicy(db, newRetentionPolicySpec(rp, 1, 0), true); err != nil {
			t.Fatal(err)
		}

		if _, err := s.Write(db, rp, "myseries,host=A value=23 1000000", nil); err != nil {
			t.Fatalf("failed to write: %s", err)
		}

		// wait for the snapshot to write
		time.Sleep(time.Second)

		if _, err := s.Write(db, rp, "myseries,host=B value=24 5000000", nil); err != nil {
			t.Fatalf("failed to write: %s", err)
		}

		// wait for the snapshot to write
		time.Sleep(time.Second)

		if _, err := s.Write(db, rp, "myseries,host=C value=25 9000000", nil); err != nil {
			t.Fatalf("failed to write: %s", err)
		}

		// wait for the snapshot to write
		time.Sleep(time.Second)

		res, err := s.Query(`show series on mydb; show retention policies on mydb`)

		res, err = s.Query(`select * from "mydb"."forever"."myseries"`)
		if err != nil {
			t.Fatalf("error querying: %s", err.Error())
		}
		if res != expected {
			t.Fatalf("query results wrong:\n\texp: %s\n\tgot: %s", expected, res)
		}

		// now backup
		cmd := backup.NewCommand()
		_, port, err := net.SplitHostPort(config.BindAddress)
		if err != nil {
			t.Fatal(err)
		}
		hostAddress := net.JoinHostPort("localhost", port)
		if err := cmd.Run("-host", hostAddress, "-database", "mydb", fullBackupDir); err != nil {
			t.Fatalf("error backing up: %s, hostAddress: %s", err.Error(), hostAddress)
		}

		if err := cmd.Run("-host", hostAddress, "-database", "mydb", "-start", "1970-01-01T00:00:00.001Z", "-end", "1970-01-01T00:00:00.007Z", partialBackupDir); err != nil {
			t.Fatalf("error backing up: %s, hostAddress: %s", err.Error(), hostAddress)
		}
	}()

	if _, err := os.Stat(config.Meta.Dir); err == nil || !os.IsNotExist(err) {
		t.Fatalf("meta dir should be deleted")
	}

	if _, err := os.Stat(config.Data.Dir); err == nil || !os.IsNotExist(err) {
		t.Fatalf("meta dir should be deleted")
	}

	// restore
	cmd := restore.NewCommand()

	if err := cmd.Run("-metadir", config.Meta.Dir, "-datadir", config.Data.Dir, "-database", "mydb", fullBackupDir); err != nil {
		t.Fatalf("error restoring: %s", err.Error())
	}

	// Make sure node.json was restored
	nodePath := filepath.Join(config.Meta.Dir, "node.json")
	if _, err := os.Stat(nodePath); err != nil || os.IsNotExist(err) {
		t.Fatalf("node.json should exist")
	}

	// now open it up and verify we're good
	s := OpenServer(config)
	defer s.Close()

	res, err := s.Query(`select * from "mydb"."forever"."myseries"`)
	if err != nil {
		t.Fatalf("error querying: %s", err.Error())
	}
	if res != expected {
		t.Fatalf("query results wrong:\n\texp: %s\n\tgot: %s", expected, res)
	}

	_, port, err := net.SplitHostPort(config.BindAddress)
	if err != nil {
		t.Fatal(err)
	}
	hostAddress := net.JoinHostPort("localhost", port)
	cmd.Run("-host", hostAddress, "-online", "-newdb", "mydbbak", "-origindb", "mydb", partialBackupDir)
	res, err = s.Query(`show databases`)

	// wait for the import to finish, and unlock the shard engine.
	time.Sleep(time.Second)

	res, err = s.Query(`select * from "mydbbak"."forever"."myseries"`)
	if err != nil {
		t.Fatalf("error querying: %s", err.Error())
	}

	if res != partialExpected {
		t.Fatalf("query results wrong:\n\texp: %s\n\tgot: %s", partialExpected, res)
	}

	fmt.Printf("result: %v", res)
}

func freePort() string {
	l, _ := net.Listen("tcp", "")
	defer l.Close()
	return l.Addr().String()
}
