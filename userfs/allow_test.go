package userfs

import (
	"testing"
	"time"
)

func TestAllows(t *testing.T) {
	var allows allows

	t.Run("non existing pid", func(t *testing.T) {
		if _, ok := allows.ForPID(11); ok {
			t.Error("found pid")
		}
	})

	t.Run("add pid, check", func(t *testing.T) {
		allows.Set(12, UserAllowAll)
		if x, _ := allows.ForPID(12); x != UserAllowAll {
			t.Error("wrong allow for pid")
		}
	})

}

func TestHourTime(t *testing.T) {
	now := time.Now()
	hnow := toHourTime(now)
	t.Logf("%s ~ %s", hnow, now)

	if now.Before(hnow.Time().Add(-time.Hour)) || now.After(hnow.Time().Add(time.Hour)) {
		t.Errorf("%s is not close enough to %s", hnow, now)
	}

	times := []hourTime{
		toHourTime(now.Add(-48 * time.Hour)),
		toHourTime(now.Add(-30 * time.Hour)),
		toHourTime(now.Add(-20 * time.Hour)),
		toHourTime(now.Add(-15 * time.Hour)),
		toHourTime(now.Add(-7 * time.Hour)),
		toHourTime(now.Add(-3 * time.Hour)),
		toHourTime(now.Add(-1 * time.Hour)),
		toHourTime(now),
		toHourTime(now.Add(1 * time.Hour)),
		toHourTime(now.Add(3 * time.Hour)),
		toHourTime(now.Add(7 * time.Hour)),
		toHourTime(now.Add(15 * time.Hour)),
		toHourTime(now.Add(20 * time.Hour)),
		toHourTime(now.Add(30 * time.Hour)),
		toHourTime(now.Add(48 * time.Hour)),
	}
	var hlast hourTime
	for _, h := range times {
		if !hlast.Time().Before(h.Time()) {
			t.Errorf("%s is not before %s", hlast, h)
		}
		hlast = h
	}
}
