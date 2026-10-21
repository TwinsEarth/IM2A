// 测试辅助（非 *.test.mjs，不会被 scripts/test.mjs 当作测试加载）：
// 提供可手动推进的假时钟，保证超时/日切类断言确定性。
export function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.set = (iso) => {
    currentMs = Date.parse(iso);
    return clock;
  };
  clock.currentMs = () => currentMs;
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}
