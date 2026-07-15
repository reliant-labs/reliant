import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 03: WhyNow. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "why-now")!;
export default function Slide03WhyNow() {
  return <SlideLayout data={data} />;
}
