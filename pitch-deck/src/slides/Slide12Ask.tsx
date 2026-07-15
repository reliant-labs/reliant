import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 12: Ask. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "ask")!;
export default function Slide12Ask() {
  return <SlideLayout data={data} />;
}
