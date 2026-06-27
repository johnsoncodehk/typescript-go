const greeting: string = "hello tsgo-napi";
const count: number = 42;

interface Point {
  x: number;
  y: number;
}

function add(a: number, b: number): number {
  return a + b;
}

const p: Point = { x: 1, y: 2 };
const total = add(p.x, p.y);

export { add, Point };
