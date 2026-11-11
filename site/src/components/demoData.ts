/** Демо-данные для мока дашборда на лендинге. */
export const demoRating = [
  { loc: "Амстердам", colo: "AMS", n: 9, avg: 113.9, zone: "ok", ping: 94 },
  { loc: "Франкфурт", colo: "FRA", n: 11, avg: 78.8, zone: "ok", ping: 88 },
  { loc: "Стокгольм", colo: "ARN", n: 12, avg: 54.9, zone: "warn", ping: 92 },
  { loc: "Хельсинки", colo: "HEL", n: 7, avg: 27.4, zone: "warn", ping: 123 },
];

/** Суточный профиль: ночью быстрее, вечером хуже — как в жизни. */
export const demoHours = Array.from({ length: 24 }, (_, h) => {
  const hf = 1.3 - 0.5 * (h >= 18 && h < 23 ? 1 : h >= 12 && h < 18 ? 0.5 : 0);
  const v = 90 * hf + (h % 5) * 3;
  return { hour: h, avg: v, cnt: 2 + (h % 4), zone: v >= 50 ? "ok" : "warn" };
});

export const demoHist = [
  { tm: "22:41:47", loc: "Амстердам", down: 87.4, zone: "ok", dur: 11 },
  { tm: "22:31:02", loc: "Франкфурт", down: 64.2, zone: "ok", dur: 12 },
  { tm: "22:21:15", loc: "Хельсинки", down: 17.8, zone: "bad", dur: 13 },
  { tm: "22:11:30", loc: "Стокгольм", down: 46.1, zone: "warn", dur: 10 },
];

export const zoneColor: Record<string, string> = {
  ok: "var(--color-ok)",
  warn: "var(--color-warn)",
  bad: "var(--color-bad)",
};
