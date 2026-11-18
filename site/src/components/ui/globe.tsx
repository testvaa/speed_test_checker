import earthUrl from "@/assets/earth.jpg";

// звёзды вокруг диска: координаты относительно контейнера 250×250
const stars = [
  { x: -14, y: 24, dur: 3, delay: 0 },
  { x: -26, y: 118, dur: 2.4, delay: 0.6 },
  { x: 262, y: 60, dur: 3.4, delay: 1.1 },
  { x: 250, y: 176, dur: 2.8, delay: 0.3 },
  { x: 44, y: -20, dur: 3.2, delay: 0.9 },
  { x: 168, y: -28, dur: 2.2, delay: 0 },
  { x: 214, y: 262, dur: 3.6, delay: 0.5 },
  { x: 8, y: 258, dur: 2.6, delay: 1.4 },
];

/** Вращающийся глобус (порт ruixen.ui/globe): полоса текстуры Земли едет под
    круглой маской, объём — внутренними тенями, вокруг мерцают «звёзды».
    Текстура лежит локально (site/src/assets/earth.jpg), без внешних CDN. */
export function Globe() {
  return (
    <div className="flex h-screen items-center justify-center">
      <style>{`
        @keyframes earthRotate { 0% { background-position: 0 0; } 100% { background-position: 400px 0; } }
        @keyframes twinkle { 0%, 100% { opacity: 0.1; } 50% { opacity: 1; } }
      `}</style>
      <div className="relative h-[250px] w-[250px]">
        {stars.map((s, i) => (
          <span
            key={i}
            aria-hidden
            className="absolute h-1 w-1 rounded-full bg-white motion-reduce:animate-none"
            style={{
              left: s.x + "px",
              top: s.y + "px",
              animation: `twinkle ${s.dur}s ${s.delay}s infinite`,
            }}
          />
        ))}
        <div
          className="absolute inset-0 overflow-hidden rounded-full motion-reduce:animate-none
            shadow-[0_0_20px_rgba(255,255,255,0.2),-5px_0_8px_#c3f4ff_inset,15px_2px_25px_#000_inset,-24px_-2px_34px_#c3f4ff99_inset,250px_0_44px_#00000066_inset,150px_0_38px_#000000aa_inset]"
          style={{
            backgroundImage: `url(${earthUrl})`,
            backgroundSize: "cover",
            backgroundPosition: "left",
            animation: "earthRotate 30s linear infinite",
          }}
        />
      </div>
    </div>
  );
}
