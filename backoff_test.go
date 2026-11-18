package main

// Тест паузы автопроверок после 429: интервал × 3 за каждую ошибку
// подряд, не более ×9 и не более 4 часов.

import "testing"

func TestBackoffMinutes(t *testing.T) {
	cases := []struct {
		intervalMin, strikes, want int
	}{
		{10, 0, 10},   // без ошибок — обычный интервал
		{10, -3, 10},  // отрицательные трактуем как ноль
		{10, 1, 30},   // первая 429: ×3
		{10, 2, 60},   // вторая подряд: ×6
		{10, 3, 90},   // третья и дальше: ×9 (потолок множителя)
		{10, 7, 90},   // серия длинная — всё ещё ×9
		{10, 100, 90}, //
		{180, 3, 240}, // 180×9=1620, но потолок — 4 часа
		{30, 2, 180},  // 30×6 без потолка
	}
	for _, c := range cases {
		if got := backoffMinutes(c.intervalMin, c.strikes); got != c.want {
			t.Errorf("backoffMinutes(%d, %d) = %d, хочу %d",
				c.intervalMin, c.strikes, got, c.want)
		}
	}
}
