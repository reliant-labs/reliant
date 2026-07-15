import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 04: Product. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "product")!;
export default function Slide04Product() {
  return <SlideLayout data={data} />;
}
