import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 09: BusinessModel. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "business-model")!;
export default function Slide09BusinessModel() {
  return <SlideLayout data={data} />;
}
